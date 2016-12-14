# ingress53

# Building

If you need to build manually, you will need to install [glide](https://glide.sh/).

```
$ git clone git@github.com:utilitywarehouse/ingress-route53-registrator.git
$ cd ingress-route53-registrator
$ glide i
$ go build .
```

Alternatively, you can build the docker image and use the binary in a container.

# TODO
- enable `-race` tests
- keep track of 'managed' records (optionally don't change records that are not managed) and cleanup stale entries
- add metrics endpoint
- integrate with `go-operational`
