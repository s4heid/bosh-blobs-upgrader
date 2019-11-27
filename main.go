package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dpb587/dynamic-metalink-resource/api"
	"github.com/dpb587/metalink"
	"github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
)

// ResourceConfig .
type ResourceConfig struct {
	Source  Source  `yaml:"source"`
	Version Version `yaml:"version"`
}

// Source .
type Source struct {
	VersionCheck string `yaml:"version_check"`
	MetalinkGet  string `yaml:"metalink_get"`
	Version      string `yaml:"version,omitempty"`
}

// Version .
type Version struct {
	Version string `json:"version"`
}

// Metadata .
type Metadata struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Blob .
type Blob struct {
	Path        string
	PackageName string
	ID          string `yaml:"object_id"`
	Size        string `yaml:"size"`
	Sha         string `yaml:"sha"`
}

// Blobs .
type Blobs map[string]*Blob

func sha256sum(filepath string) (string, error) {
	f, err := os.Open(filepath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		log.Fatal(err)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), err
}

// DownloadFile will download a url to a local file
func DownloadFile(filepath, url string) (Blob, error) {
	fmt.Printf("Downloading %s from %s\n", filepath, url)

	var blob Blob
	resp, err := http.Get(url)
	if err != nil {
		return blob, err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return blob, err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)

	sha, err := sha256sum(filepath)
	if err != nil {
		return blob, fmt.Errorf("calculating shasum: %v", err)
	}
	blob.Sha = fmt.Sprintf("sha256:%s", sha)

	return blob, err
}

// Unmarshal .
func (s *Blobs) Unmarshal(data []byte) error {
	err := yaml.NewDecoder(bytes.NewReader(data)).Decode(s)
	if err != nil {
		return err
	}
	for k, v := range *s {
		v.Path = strings.TrimSpace(string(k))
		v.PackageName = strings.Split(v.Path, "/")[0]
	}
	return nil
}

func boshBinaryPath() (string, error) {
	var err error
	binaryPath := os.Getenv("BOSH_BINARY_PATH")
	if binaryPath != "" {
		return binaryPath, nil
	}
	binaryPath, err = exec.LookPath("bosh")
	if err != nil {
		return "", fmt.Errorf("lookup bosh binary in PATH: %v", err)
	}
	return binaryPath, nil
}

func boshRemoveBlob(boshBinaryPath, blobPath, releaseDir string) error {
	cmd := exec.Command(
		boshBinaryPath,
		"remove-blob",
		fmt.Sprintf("--dir=%s", releaseDir),
		blobPath,
	)

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("run boshRemoveBlob command: %v", err)
	}

	return nil
}

func boshAddBlob(boshBinaryPath, blobPath, blobName, releaseDir string) error {
	cmd := exec.Command(
		boshBinaryPath,
		"add-blob",
		fmt.Sprintf("--dir=%s", releaseDir),
		blobPath,
		blobName,
	)

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("run boshAddBlob command: %v", err)
	}

	return nil
}

func main() {
	var err error
	var releaseDir string

	if len(os.Args) == 2 {
		releaseDir = os.Args[1]
	} else {
		releaseDir, err = os.Getwd()
		if err != nil {
			panic(err)
		}
	}

	blobsData, err := ioutil.ReadFile(filepath.Join(releaseDir, "config", "blobs.yml"))
	if err != nil {
		panic(err)
	}

	var blobs Blobs = map[string]*Blob{}
	err = blobs.Unmarshal([]byte(blobsData))
	if err != nil {
		log.Fatalf("decoding blobs file: %v", err)
	}

	var boshBinary string
	boshBinary, err = boshBinaryPath()
	if err != nil {
		panic(err)
	}

	resourcePaths, err := filepath.Glob(filepath.Join(releaseDir, "config", "blobs", "*", "resource.yml"))
	if err != nil {
		panic(err)
	}
	for _, r := range resourcePaths {
		localBlobPath := filepath.Dir(r)
		packageName := filepath.Base(localBlobPath)
		repositoryBytes, err := ioutil.ReadFile(r)
		if err != nil {
			panic(err)
		}

		var resourceConfig ResourceConfig
		err = yaml.Unmarshal(repositoryBytes, &resourceConfig)
		if err != nil {
			panic(err)
		}

		stdout, err := api.ExecuteScript(resourceConfig.Source.VersionCheck, nil)
		if err != nil {
			panic(err)
		}
		versionsList := strings.Split(strings.TrimSpace(string(stdout)), "\n")
		latestVersion, err := version.NewVersion(versionsList[0])
		for i, rawVersion := range versionsList {
			if rawVersion == "" || i == 0 {
				continue
			}
			v, _ := version.NewVersion(rawVersion)
			if latestVersion.LessThan(v) {
				fmt.Printf("%s is less than %s", latestVersion, v)
				latestVersion = v
			}
		}

		meta4Bytes, err := api.ExecuteScript(resourceConfig.Source.MetalinkGet, map[string]string{
			"version": latestVersion.String(),
		})
		if err != nil {
			errors.Wrap(err, "executing metalink_get script")
		}
		var meta4 metalink.Metalink
		err = metalink.Unmarshal(meta4Bytes, &meta4)
		if err != nil {
			errors.Wrap(err, "unmarshaling metalinks")
		}

		if len(meta4.Files) > 1 {
			panic("more than one metalink file is currently not supported.")
		}
		file := meta4.Files[0]
		if len(file.URLs) > 1 {
			panic("more than one metalink URL per file is currently not supported.")
		}

		// compare latest upstream version with version from blobs.yml
		for _, b := range blobs {

			if b.PackageName != packageName {
				continue
			}
			fmt.Printf("%v, %v\n", b.Path, b.Sha)

			var newBlob Blob
			newBlob, err = DownloadFile(filepath.Join(localBlobPath, file.Name), file.URLs[0].URL)
			if err != nil {
				panic(err)
			}

			if b.Sha == newBlob.Sha {
				// blob did not change, nothing to do
				continue
			}

			newBlob.Path = fmt.Sprintf("%s/%s", packageName, file.Name)
			fmt.Printf("Upgrading blob: %s (%s) --> %s (%s)\n", b.Path, b.Sha, newBlob.Path, newBlob.Sha)

			err = boshRemoveBlob(boshBinary, b.Path, releaseDir)
			if err != nil {
				panic(err)
			}

			// TODO: somehow boshAddBlob adds different digest than sha256sum
			err = boshAddBlob(boshBinary, r, newBlob.Path, releaseDir)
			if err != nil {
				panic(err)
			}
		}
	}
}
