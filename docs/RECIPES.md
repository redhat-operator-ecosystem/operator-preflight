# Preflight Usage Examples

Below are detailed examples on how to run `preflight` in various
environments.

## Operator Policy
These examples are shown using the Operator policy against an operator bundle
(e.g. `preflight check operator <bundle>`).

You will also need an index image containing your bundle for each of these approaches.
See [DOCS](BUILDING_AN_INDEX.md)

### As a Binary on Your Workstation

To run `preflight` on your workstation, you'll first need to download and
[install](../README.md#Installation)
the binary to your path.

You will also need:

- Your bundle image published to a container registry,
- An Index Image containing your operator's new bundle published to a container
  registry.
- A Kubeconfig for a user with cluster-admin privileges to an OpenShift 4.5+
  Cluster running Operator Lifecycle Manager.

The `preflight` tool will use the above to execute the Operator policy against
your test asset. With the above in hand, you can execute preflight against your
test asset by executing the following.

```bash
export KUBECONFIG=/path/to/your/kubeconfig 
export PFLT_INDEXIMAGE=registry.example.org/your-namespace/your-index-image:sometag
preflight check operator registry.example.org/your-namespace/your-bundle-image:sometag
```

### Using Podman (or Docker)

Running `preflight` in a Podman or Docker container is very similar to running
it on your workstation, but you will likely want to leverage a few volume mounts
to pass through various artifacts back to your host system for review at a later
time.

The below example has had its container-tool abstraced out, but you should be
able to run this with either Podman or Docker without issue.

Here, we explicitly set the location in the container where we would like
artifacts and logfiles to be written by using the `PFLT_ARTIFACTS` and
`PFLT_LOGFILE` environment variables. Then we bind host volumes to these
locations so that the data will be preserved when the container completes (the
container will be deleted after completion due to the `--rm` flag).

```bash
CONTAINER_TOOL=podman
$CONTAINER_TOOL run \
  -it \
  --rm \
  --security-opt=label=disable \
  --env KUBECONFIG=/kubeconfig \
  --env PFLT_LOGLEVEL=trace \
  --env PFLT_INDEXIMAGE=registry.example.org/your-namespace/your-index-image:sometag \
  --env PFLT_ARTIFACTS=/artifacts \
  --env PFLT_CHANNEL=beta \
  --env PFLT_LOGFILE=/artifacts/preflight.log \
  -v /some/path/on/your/host/artifacts:/artifacts \
  -v /some/path/on/your/host/kubeconfig:/kubeconfig:ro \
  quay.io/opdev/preflight:stable check operator registry.example.org/your-namespace/your-bundle-image:sometag
```

### As a Job In OpenShift (or Kubernetes)

You should be able to run `preflight` as a job in OpenShift without requiring
additional privileges or security context constraints.

As with previous examples, you will need to provide `preflight`a Kubeconfig
mapping a user with cluster-admin privileges to an OpenShift cluster that can be
used for tests.

In the namespace you will be creating this job, provision a secret with the
Kubeconfig:

```shell
oc create secret generic test-cluster-kubeconfig --from-file=kubeconfig=/some/path/on/your/host/kubeconfig
```

Then, create a Job manifest on your system. Note that you will need to
substitute:

- your bundle path in the `command` array
- your index image in the `env` array
- the volume type for the `outputdir` volume (if you want to use an alternate
  storage provider)

```yaml
echo >> preflight.yaml <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: preflight
spec:
  template:
    spec:
      containers:
      - name: preflight
        image: "quay.io/opdev/preflight:stable"
        command: ["preflight", "check", "operator", "registry.example.org/your-namespace/your-bundle-image:sometag"]
        env:
          - name: KUBECONFIG
            value: "/creds/kubeconfig"
          - name: PFLT_LOGLEVEL
            value: trace
          - name: PFLT_INDEXIMAGE
            value: "registry.example.org/your-namespace/your-index-image:sometag"
          - name: PFLT_LOGFILE
            value: "/artifacts/preflight.log"
          - name: PFLT_ARTIFACTS
            value: "/artifacts"
          - name: PFLT_CHANNEL
            value: "beta"
        volumeMounts:
          - name: "outputdir"
            mountPath: "/artifacts"
          - name: "kubeconfig"
            mountPath: "/creds"
      restartPolicy: Never
      volumes:
        - name: "outputdir"
          emptyDir:
            medium: ""
        - name: kubeconfig
          secret:
            secretName: test-cluster-kubeconfig
            optional: false
  backoffLimit: 2
EOF
```

```shell
oc apply -f preflight.yaml
```

## Container Policy
These examples are shown using the Container policy against a container image
(e.g. `preflight check container <image>`). Container policy only runs as a binary on your workstation. Check the latest
[release](https://github.com/redhat-openshift-ecosystem/openshift-preflight/releases) for the binary that matches your operating system.

You will also need:
- Your container image published to a container registry
  - An example would be `quay.io/repo-name/container-name:version`
- A Certification Component ID of the component that was set up in Red Hat Partner Connect
  - This value can be obtained from the Overview page's URL
    - For the following example Overview URL of `https://connect.redhat.com/components/1234567890aabbccddeeffgg/overview`
      - The Certification Component ID would be: `1234567890aabbccddeeffgg`
  - Required for submit
- A Partner Connect API Key
  - An API Key can be created in Red Hat Partner Connect at the following [URL](https://connect.redhat.com/account/api-keys)
  - Required for submit
- A Docker Auth file, can be specified multiple ways
  - If PFLT_DOCKERCONFIG envvar is set, or --docker-config is passed on the command line, this will be used to pull images.
    If also submitting, the file specified will be sent to Red Hat. If the image under test is on a public registry, there is
    no need to pass this parameter.

### Testing a Container
Running container policy checks against a container iteratively until all tests pass.

```bash
preflight check container registry.example.org/your-namespace/your-image:sometag
```

### Submitting a Container's Test Results to Red Hat
Running container policy checks against a container that has passed all tests and results need to be submitted to Red Hat.

```bash
preflight check container registry.example.org/your-namespace/your-image:sometag \
--submit \
--pyxis-api-token=abcdefghijklmnopqrstuvwxyz123456 \
--certification-component-id=1234567890a987654321bcde \
--docker-config=/path/to/your/dockerconfig 
```

### Testing Container and Passing Parameters in the Config File
To avoid displaying the Pyxis token in the console, you may pass it in the config file. First, add config.yaml in the directory with the Preflight binary

```bash
$ cat config.yaml
dockerConfig: path/to/config.json
loglevel: trace
logfile: artifacts/preflight.log
artifacts: artifacts
junit: true
certification_component_id: my_nice_component_id
pyxis_api_token: my_nice_token
```

and then run Preflight.

```bash
preflight \
check container \
your-image:sometag \
--submit
```

### Using Podman on a RHEL host

Here, we explicitly set the location in the container where we would like
artifacts and logfiles to be written by using the `PFLT_ARTIFACTS` and
`PFLT_LOGFILE` environment variables. Then we bind host volumes to these
locations so that the data will be preserved when the container completes (the
container will be deleted after completion due to the `--rm` flag). We also make
the assumption that the user would like to submit results to Red Hat; meaning `PFLT_CERTIFICATION_COMPONENT_ID`,
`PFLT_PYXIS_API_TOKEN` and `PFLT_DOCKERCONFIG` need to be provided. **Note:** The docker config
provided to the `PFLT_DOCKERCONFIG` environment should be from the following command:
`podman login --username [USERNAME] --password [PASSWORD] --authfile ./temp-authfile.json [REGISTRY]`

```bash
CONTAINER_TOOL=podman
$CONTAINER_TOOL run \
  -it \
  --rm \
  --security-opt=label=disable \
  --env PFLT_LOGLEVEL=trace \
  --env PFLT_ARTIFACTS=/artifacts \
  --env PFLT_LOGFILE=/artifacts/preflight.log \
  --env PFLT_CERTIFICATION_COMPONENT_ID=1234567890a987654321bcde \
  --env PFLT_PYXIS_API_TOKEN=abcdefghijklmnopqrstuvwxyz123456 \
  --env PFLT_DOCKERCONFIG=/temp-authfile.json \
  -v /some/path/on/your/host/artifacts:/artifacts \
  -v ./temp-authfile.json:/temp-authfile.json:ro \
  quay.io/opdev/preflight:stable check container registry.example.org/your-namespace/your-bundle-image:sometag --submit
```

### Testing a local container, i.e. not yet pushed to a registry

Preflight does not support certifying against a local image, that is not pushed to
a registry. In some cases, like a CI system, it is desirable to run preflight against
an image BEFORE pushing to a public registry. In order to support that, one should
start a local registry, push to it, and point preflight at the local registry.

```bash
podman run -p 5000:5000 docker.io/library/registry
podman push --tls-verify=false localhost/myrepo/mycontainer:v1.0 localhost:5000/myrepo/mycontainer:v1.0
preflight check container --insecure localhost:5000/myrepo/mycontainer:v1.0
```

Note: --submit and --insecure are mutually exclusive. A container cannot be fully
certified and submitted unless it is on a secure registry.
