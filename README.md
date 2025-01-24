# HearthHub Sidecar
A sidecar container which persists world files, backups, and metrics to S3 for a Valheim dedicated server. This application
is designed to be run in a container which is on the **same** pod as the Valheim Dedicated Server.

## Building

To build the docker image  for the Valheim server run: `./build.sh 0.0.1` replacing `0.0.1` with
the image version you would like to use.

## Deployment

This container is not designed to be deployed in an isolated manner as it depends on volumes and AWS credentials being
present on the Valheim dedicated server pod. Therefore, it should be integrated and updated with the [HearthHub mod API](https://github.com/cbartram/hearthhub-mod-api) 
which is responsible for actually creating the server deployment on Kubernetes.

## Built With

- [Kubernetes](https://kubernetes.io) - Container orchestration platform
- [Helm](https://helm.sh) - Manages Kubernetes deployments
- [Docker](https://docker.io/) - Container build tool
- [Steam](https://steam.com) - CLI used to install Valheim dedicated server

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
