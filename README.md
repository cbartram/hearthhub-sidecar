<p align="center">
   <img src="./resources/logo.webp" height="300" width="300">
</p>

[![Contributors][contributors-shield]][contributors-url]
[![Forks][forks-shield]][forks-url]
[![Stargazers][stars-shield]][stars-url]
[![Issues][issues-shield]][issues-url]
[![MIT License][license-shield]][license-url]

# HearthHub Sidecar

A sidecar container which persists world files, backups, and metrics to S3 for a Valheim dedicated server. This application
is designed to be run in a container which is on the **same** pod as the Valheim Dedicated Server. 

See [HearthHub Kube Api's](https://github.com/cbartram/hearthhub-kube-api) create server handler file for more information on how the sidecar is deployed.

## Building

To build the docker image  for the Valheim server run: `./build.sh 0.0.1` replacing `0.0.1` with
the image version you would like to use.


## Configuration

Configuration for the sidecar is managed through environment variables (often loaded from a `ConfigMap`):

- `BACKUP_FREQUENCY_MIN` - The duration in minutes when backups to S3 should occur.
- `CLEANUP_FREQUENCY_MIN`- The duration in minutes when the cleanup process should run see "[cleanup](#cleanup-process)" for more details.
- `MAX_BACKUPS` - The maximum number of backups to keep in S3 defaults to 3. Additional backups will be purged by the [cleanup process](#cleanup-process).
- `S3_BUCKET` - The bucket name (without s3:// prefix) where backups should be stored.

## Deployment

This container is not designed to be deployed in an isolated manner as it depends on volumes and AWS credentials being
present on the Valheim dedicated server pod. See the [Dockerfile]()https://github.com/cbartram/hearthhub-kube-api/blob/master/Dockerfile that runs the Valheim dedicated server.

Therefore, it should be integrated and updated with the [HearthHub mod API](https://github.com/cbartram/hearthhub-mod-api) 
which is responsible for actually creating the server deployment on Kubernetes.

To build and run it locally you can use: `go build -o main .` but there is no guarantee this will function as expected
since the filesystem will likely differ.

## Cleanup Process

The cleanup process is a configurable process which will purge the latest `MAX_BACKUPS` .fwl and .db file pairs from
S3. The process will also delete any orphaned .fwl files without a .db pair.

## Built With

- [Kubernetes](https://kubernetes.io) - Container orchestration platform
- [Helm](https://helm.sh) - Manages Kubernetes deployments
- [Docker](https://docker.io/) - Container build tool

## Contributing

Please read [CONTRIBUTING.md](CONTRIBUTING.md) for details on our code
of conduct, and the process for submitting pull requests to us.

## Versioning

We use [Semantic Versioning](http://semver.org/) for versioning. For the versions
available, see the [tags on this
repository](https://github.com/cbartran/hearthhub-mod-api/tags).

## Authors

- **cbartram** - *Initial work* -
  [cbartram](https://github.com/cbartram)

## License

This project is licensed under the [CC0 1.0 Universal](LICENSE)
Creative Commons License - see the [LICENSE.md](LICENSE) file for
details

[contributors-shield]: https://img.shields.io/github/contributors/cbartram/hearthhub-sidecar.svg?style=for-the-badge
[contributors-url]: https://github.com/cbartram/hearthhub-sidecar/graphs/contributors
[forks-shield]: https://img.shields.io/github/forks/cbartram/hearthhub-sidecar.svg?style=for-the-badge
[forks-url]: https://github.com/cbartram/hearthhub-sidecar/network/members
[stars-shield]: https://img.shields.io/github/stars/cbartram/hearthhub-sidecar.svg?style=for-the-badge
[stars-url]: https://github.com/cbartram/hearthhub-sidecar/stargazers
[issues-shield]: https://img.shields.io/github/issues/cbartram/hearthhub-sidecar.svg?style=for-the-badge
[issues-url]: https://github.com/cbartram/hearthhub-sidecar/issues
[license-shield]: https://img.shields.io/github/license/cbartram/hearthhub-sidecar.svg?style=for-the-badge
[license-url]: https://github.com/cbartram/hearthhub-sidecar/blob/master/LICENSE
