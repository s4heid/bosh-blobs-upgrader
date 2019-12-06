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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
	"time"

	boshcmd "github.com/cloudfoundry/bosh-cli/cmd"
	bilog "github.com/cloudfoundry/bosh-cli/logger"
	boshui "github.com/cloudfoundry/bosh-cli/ui"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"

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

	err = os.Chmod(filepath, 0777)
	if err != nil {
		return blob, fmt.Errorf("changing permissions: %v", err)
	}

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

func getFromEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getStrictFromEnv(key string) (string, error) {
	if value, ok := os.LookupEnv(key); ok {
		return value, nil
	}
	return "", errors.New(fmt.Sprintf("variable %q not set in environment", key))
}

func bosh(args []string) error {
	level := boshlog.LevelNone
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	logger, _ := bilog.NewSignalableLogger(boshlog.NewLogger(level), c)

	ui := boshui.NewConfUI(logger)
	defer ui.Flush()

	cmdFactory := boshcmd.NewFactory(boshcmd.NewBasicDeps(ui, logger))

	cmd, err := cmdFactory.New(args)
	if err != nil {
		panic(err)
	}

	return cmd.Execute()
}

func boshAddBlob(filePath, blobPath, releaseDir string) error {
	return bosh([]string{"add-blob", fmt.Sprintf("--dir=%s", releaseDir), filePath, blobPath})
}

func boshRemoveBlob(blobPath, releaseDir string) error {
	return bosh([]string{"remove-blob", fmt.Sprintf("--dir=%s", releaseDir), blobPath})
}

func boshUploadBlobs(releaseDir string) error {
	return bosh([]string{"upload-blobs", fmt.Sprintf("--dir=%s", releaseDir)})
}

func main() {
	var (
		err          error
		releaseDir   string
		funcs        = template.FuncMap{"join": strings.Join}
		updatedBlobs = []string{}
	)
	const master = `Updates:{{block "list" .}}{{"\n"}}{{range .}}{{println "-" .}}{{end}}{{end}}`

	if len(os.Args) == 2 {
		releaseDir = os.Args[1]
	} else {
		releaseDir, err = os.Getwd()
		if err != nil {
			panic(err)
		}
	}

	r, err := git.PlainOpen(releaseDir)
	if err != nil {
		panic(err)
	}
	w, err := r.Worktree()
	if err != nil {
		panic(err)
	}

	// headRef, err := r.Head()
	// updateBranch := plumbing.NewBranchReferenceName(getFromEnv("GIT_UPDATE_BRANCH", "bosh-blobs-upgrader"))
	// ref := plumbing.NewHashReference(updateBranch, headRef.Hash())
	// err = r.Storer.SetReference(ref)
	// if err != nil {
	// 	panic(err)
	// }

	os.Setenv("BOSH_NON_INTERACTIVE", "true")

	blobsData, err := ioutil.ReadFile(filepath.Join(releaseDir, "config", "blobs.yml"))
	if err != nil {
		panic(err)
	}

	var blobs Blobs = map[string]*Blob{}
	err = blobs.Unmarshal([]byte(blobsData))
	if err != nil {
		log.Fatalf("decoding blobs file: %v", err)
	}

	resourcePaths, err := filepath.Glob(filepath.Join(releaseDir, "config", "blobs", "*", "resource.yml"))
	if err != nil {
		panic(err)
	}
	commitHeader := "Update blobs:"
	commitBody := ""
	for _, r := range resourcePaths {
		localBlobDir := filepath.Dir(r)
		packageName := filepath.Base(localBlobDir)
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
				latestVersion = v
			}
		}

		meta4Bytes, err := api.ExecuteScript(resourceConfig.Source.MetalinkGet, map[string]string{
			"version": latestVersion.Original(),
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

		currentVersionBytes, err := ioutil.ReadFile(filepath.Join(localBlobDir, "version"))
		if err != nil && !os.IsNotExist(err) {
			panic(err)
		}

		if string(currentVersionBytes) == latestVersion.Original() {
			fmt.Printf("Skipping  package '%s'. Version is unchanged.\n", packageName)
			continue
		}

		// compare latest upstream version with version from blobs.yml
		blobFilePath := filepath.Join(localBlobDir, file.Name)
		for _, b := range blobs {

			if b.PackageName != packageName {
				continue
			}
			fmt.Printf("Checking %s (%s)\n", b.Path, b.Sha)

			var newBlob Blob
			newBlob, err = DownloadFile(blobFilePath, file.URLs[0].URL)
			if err != nil {
				panic(err)
			}

			if b.Sha == newBlob.Sha {
				fmt.Printf("Skipping package '%s'. Blobs digest '%s' did not change.\n", b.PackageName, newBlob.Sha)
				continue
			}

			newBlob.Path = fmt.Sprintf("%s/%s", packageName, file.Name)
			commitHeader += fmt.Sprintf(" %s", packageName)
			commitBody += fmt.Sprintf(" - %q --> %q\n", b.Path, newBlob.Path)
			updatedBlobs = append(updatedBlobs, fmt.Sprintf("%s %s (from %s)\n", b.PackageName, latestVersion.Original(), currentVersionBytes))
			fmt.Printf("Upgrading blob: %s (%s) --> %s (%s)\n", b.Path, b.Sha, newBlob.Path, newBlob.Sha)

			err = boshRemoveBlob(b.Path, releaseDir)
			if err != nil {
				panic(err)
			}

			err = boshAddBlob(blobFilePath, newBlob.Path, releaseDir)
			if err != nil {
				panic(err)
			}
		}

	}

	b := filepath.Join("config", "blobs.yml")
	_, err = w.Add(b)
	if err != nil {
		panic(err)
	}

	status, err := w.Status()
	if err != nil {
		panic(err)
	}
	fmt.Println("Status: ", status)

	if status.File(b).Staging != git.Modified {
		fmt.Println("No changes in git detected.")
		os.Exit(1)
	}

	if _, err := os.Stat(filepath.Join(releaseDir, "config", "private.yml")); os.IsNotExist(err) {
		panic(fmt.Errorf("blobstore credentials not set: %v", err))
	}
	err = boshUploadBlobs(releaseDir)
	if err != nil {
		panic(err)
	}

	changelogPath := filepath.Join(releaseDir, "config", "changelog")
	if _, err := os.Stat(changelogPath); os.IsNotExist(err) {
		os.Mkdir(changelogPath, 0755)
	}
	changelogVersion := getFromEnv("CHANGELOG_VERSION", fmt.Sprintf("%d", time.Now().Unix()))
	fh, err := os.OpenFile(filepath.Join(changelogPath, fmt.Sprintf("CHANGELOG_%s.txt", changelogVersion)), os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		fmt.Printf("create file: %v", err)
		os.Exit(1)
	}

	defer fh.Close()

	masterTmpl, _ := template.New("master").Funcs(funcs).Parse(master)
	if err := masterTmpl.Execute(fh, updatedBlobs); err != nil {
		panic(err)
	}

	_, err = w.Add(changelogPath)
	if err != nil {
		panic(err)
	}

	_, err = w.Add(b)
	if err != nil {
		panic(err)
	}

	commitMsg := commitHeader + "\n\n" + commitBody
	commit, err := w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  getFromEnv("GIT_NAME", "Dependency Bot"),
			Email: getFromEnv("GIT_EMAIL", "ci@localhost"),
			When:  time.Now(),
		},
	})
	if err != nil {
		panic(err)
	}

	obj, err := r.CommitObject(commit)
	if err != nil {
		panic(err)
	}
	fmt.Println(obj)

	// token, err := getStrictFromEnv("GITHUB_TOKEN")
	// if err != nil {
	// 	panic(err)
	// }
	// err = r.Push(&git.PushOptions{
	// 	Auth: &githttp.BasicAuth{
	// 		Username: "token",
	// 		Password: token,
	// 	},
	// 	RefSpecs: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"},
	// 	Progress: os.Stdout,
	// })
	// if err != nil {
	// 	panic(err)
	// }
}
