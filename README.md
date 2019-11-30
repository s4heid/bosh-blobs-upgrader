# bosh-blobs-upgrader

This GitHub action can be used to upgrade blobs from external, versioned resources in [BOSH](https://bosh.io) releases.

## Inputs

The following list of input arguments can be configured for now.

| Input                         | Description                                        |
|-------------------------------|----------------------------------------------------|
| repository                    | Path to the bosh-release repository                |

**Example usage:**

```yaml
- name: Upgrade Bosh Blobs
  uses: s4heid/bosh-blobs-upgrader-action@master
  with:
    repository: path/to/bosh-release
```

See [s4heid/athens-bosh-release](https://github.com/s4heid/athens-bosh-release) for an example configuration.


## Docker

The docker container can be run locally with the following command:

```sh
docker run --rm \
    -v $HOME/path/to/bosh-release:/home/path/to/bosh-release \
    docker.pkg.github.com/s4heid/bosh-blobs-upgrader-action/bosh-blobs-upgrader-action:latest\ \
    /home/path/to/bosh-release
```


## References

- [dpb587/bosh-release-blobs-upgrader-pipeline](https://github.com/dpb587/bosh-release-blobs-upgrader-pipeline)

## License

[MIT License](./LICENSE)