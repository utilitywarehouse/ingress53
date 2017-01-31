# ingress53

ingress53 is a service designed to run in kubernetes and maintain DNS records for its ingress resources on AWS Route53.

# Usage
ingress53 is slightly opinionated in that it assumes there are two kinds of ingresses: public and private. A kubernetes selector is used to select public ingresses, while all others default to being private.

You can test it locally (please refer to the command line help for more options):
```sh
./ingress53 \
    -route53-zone-id=XXXXXXXXXXXXXX \
    -target-private=private.example.com \
    -target-public=public.example.com \
    -kubernetes-config=$HOME/.kube/config \
    -dry-run
```

You can use the generated docker image (utilitywarehouse/ingress53) to deploy it on your kubernetes cluster.

## Building

If you need to build manually, you will need to install [glide](https://glide.sh/).

```
$ git clone git@github.com:utilitywarehouse/ingress53.git
$ cd ingress53
$ glide i
$ go build .
```
