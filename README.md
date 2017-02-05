# ingress53

ingress53 is a service designed to run in kubernetes and maintain DNS records for the cluster's ingress resources in AWS Route53.

It will watch the kubernetes API (using the service token) for any Ingress resource changes and try to apply those records to route53 in Amazon mapping the record to the "target name", which is the dns name of the ingress endpoint for your cluster.

# Requirements

You need to export the following env variables to be able to use AWS APIs:

```sh
export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
exoprt AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

# Usage

ingress53 is slightly opinionated in that it assumes there are two kinds of ingress endpoints: public and private. A kubernetes selector is used to select public ingresses, while all others default to being private.

You will need to create a dns record that points to your ingress endpoint[s]. We will use this to CNAME all ingress resource entries to that "target".

Your set up might look like this:

 - A ingress controller (nginx/traefik) kubernetes service running on a nodePort (:8080)
 - ELB that serves all worker nodes on :8080
 - A CNAME for the elb `private.example.com` > `my-loadbalancer-1234567890.us-west-2.elb.amazonaws.com`
 - ingress53 service running inside the cluster

Now, if you were to create an ingress kubernetes resource:

```yaml
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: my-app
spec:
  rules:
  - host: my-app.example.com
    http:
      paths:
      - path: /
        backend:
          serviceName: my-app
          servicePort: 80
```

ingress53 will create a CNAME record in route53: `my-app.example.com` > `private.example.com`

You can test it locally (please refer to the command line help for more options):

```sh
./ingress53 \
    -route53-zone-id=XXXXXXXXXXXXXX \
    -target-private=private.example.com \
    -target-public=public.example.com \
    -kubernetes-config=$HOME/.kube/config \
    -dry-run
```

You can use the generated docker image ([utilitywarehouse/ingress53](https://hub.docker.com/r/utilitywarehouse/ingress53/)) to deploy it on your kubernetes cluster.

## Example kubernetes manifests

```yaml
---
apiVersion: v1
kind: Service
metadata:
  name: ingress53
  labels:
    app: ingress53
  namespace: kube-system
  annotations:
    prometheus.io/scrape: 'true'
    prometheus.io/path:   /metrics
    prometheus.io/port:   '5000'
spec:
  ports:
  - name: web
    protocol: TCP
    port: 80
    targetPort: 5000
  selector:
    app: ingress53
---
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  labels:
    app: ingress53
  name: ingress53
  namespace: kube-system
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: ingress53
      name: ingress53
    spec:
      containers:
      - name: ingress53
        image: utilitywarehouse/ingress53:v1.0.0
        args:
          - -route53-zone-id=XXXXXX
          - -target-private=private.example.com
          - -target-public=public.example.com
          - -public-ingress-selector=ingress-tag-name:ingress-tag-value
          - -debug
          - -v=10
        resources:
          requests:
            cpu: 10m
            memory: 64Mi
        ports:
        - containerPort: 5000
          name: web
          protocol: TCP
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: ingress53
              key: aws_access_key_id
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: ingress53
              key: aws_secret_access_key
```

## Building

If you need to build manually:

```
$ git clone git@github.com:utilitywarehouse/ingress53.git
$ cd ingress53
$ go build .
```

The project uses [glide](https://glide.sh/) to manage dependencies but at the same time, they are vendored for simplicity.
