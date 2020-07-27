# Quobyte CSI

Quobyte CSI is the implementation of
 [Container Storage Interface (CSI)](https://github.com/container-storage-interface/spec/tree/release-1.0).
 Quobyte CSI enables easy integration of Quobyte Storage into Kubernetes. Current Quobyte CSI plugin
 supports the following functionality

* Dynamic Volume Create
* Volume Delete
* Pre-provisioned volumes (Delete policy does not apply to these volumes)
* Volume Expansion (Only dynamically provisioned volumes can be expanded)

## Index

* [Requirements](#requirements)
* [Deploy Quobyte clients](docs/deploy_clients.md)
* [Deploy Quobyte CSI](#deploy-quobyte-CSI)
* [Use Quobyte volumes in Kubernetes](#use-quobyte-volumes-in-kubernetes)
  * [Dynamic volume provisioning](#dynamic-volume-provisioning)
  * [Use existing volumes](#use-existing-volumes)
* [Secure storage access](docs/secure-storage-with-psp.md)
* [Uninstall Quobyte CSI](#uninstall-quobyte-csi)
* [Most common mistakes](docs/common_errors.md)
* [Collect Quobyte CSI logs](docs/collect_quobyte_csi_logs.md)

## Requirements

* Kubernetes v1.16
* Quobyte installation with reachable registry and api services from the Kubernetes nodes and pods
* Quobyte client with mount path as `/mnt/quobyte/mounts`. Please see
 [Deploy Quobyte clients](docs/deploy_clients.md) for Quobyte client installation instructions.
  *  To use Quobyte access keys quobyte client should be deployed with **--enable-access-contexts**

## Deploy Quobyte CSI

1. Clone the quobyte CSI repository from github

    Using `HTTPS`

    ```bash
    git clone https://github.com/quobyte/quobyte-csi.git
    cd quobyte-csi
    git checkout tags/v1.0.5 # checkout release v1.0.5
    ```

    Using `SSH`

    ```bash
    git clone git@github.com:quobyte/quobyte-csi.git
    cd quobyte-csi
    git checkout tags/v1.0.5 # checkout release v1.0.5
    ```

2. Helm is required to deploy the Quobyte CSI driver. Please install [Helm](https://helm.sh/docs/intro/install/#from-script) on K8S master node.

    ```bash
    curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 && chmod 700 get_helm.sh && ./get_helm.sh
    ```

3. Edit [CSI driver configuration](helm/values.yaml) Configure CSI driver with Quobyte API and other required information.

4. (optional) generate driver deployment `.yaml` and verify the configuration.

    ```bash
    helm template ./helm --debug >> csi-driver.yaml
    ```

5. Deploy Qubotye CSI driver (deploys driver with configuration from step 3)

    ```bash
    helm install ./helm
    ```

6. Verify the status of Quobyte CSI driver pods

    Deploying Quobyte CSI driver should create `csi.quobyte.com` CSIDriver
     object (this may take few seconds)

    ```bash
    kubectl get CSIDriver | grep ^csi.quobyte.com
    ```

    The Quobyte CSI plugin is ready for use, if you see `quobyte-csi-controller-x`
    pod running on any one node and `quobyte-csi-node-xxxxx`
    running on every node of the Kubernetes cluster.

    ```bash
    kubectl -n kube-system get po -owide | grep ^quobyte-csi
    ```

7. Make sure your CSI driver is running against the expected Quobyte API endpoint

    ```bash
    kubectl -n kube-system exec -it \
    "$(kubectl get po -n kube-system | grep -m 1 ^quobyte-csi-node | cut -f 1 -d' ')" \
    -c quobyte-csi-plugin -- env | grep QUOBYTE_API_URL  
    ```

    The above command should print your Quobyte API endpoint.
    After that, uninstall Quobyte CSI driver and install again.

## Use Quobyte volumes in Kubernetes

`Note:` This section uses `example/` deployment files for demonstration. These should be modified
  with your deployment configurations such as `namespace`, `quobyte registry`, `Quobyte API user credentials` etc.

We use `quobyte` namespace for the examples. Create the namespace

  ```bash
  kubectl create ns quobyte
  ```

Quobyte requires a secret to authenticate volume create and delete requests. Create this secret with
 your Quobyte API login credentials. Kubernetes requires base64 encoding for secret data which can be obtained
 with the command `echo -n "value" | base64`. Please encode your user name, password and access key
 information in base64 and update [example/csi-secret.yaml](example/csi-secret.yaml)

  ```bash
  kubectl create -f example/csi-secret.yaml
  ```

Create a [storage class](example/StorageClass.yaml) with the `provisioner` set to `csi.quobyte.com` along with other configuration
 parameters. You could create multiple storage classes by varying `parameters` such as
  `quobyteTenant`, `quobyteConfig` etc. If you're using `Quobyte access keys`, make sure to configure the `user:`, `group:` and `accessMode:` for the volume as required.  

  ```bash
  kubectl create -f example/StorageClass.yaml
  ```

To run the **Nginx demo** pods,

1. Host nodes must have nginx user (UID: 101) and group (GID: 101). Please
 create nginx user and group on every node.

    ```bash
    sudo groupadd -g 101 nginx; sudo useradd -u 101 -g 101 nginx
    ```

2. `nginx` user must have at least read and execute permissions on the volume

### Dynamic volume provisioning

Creating a PVC referencing the storage class created in the previous step would provision dynamic
 volume. The secret `csi.storage.k8s.io/provisioner-secret-name` from the namespace `csi.storage.k8s.io/provisioner-secret-namespace`
 in the referenced StorageClass will be used to authenticate volume creation and deletion.

1. Create [PVC](example/pvc-dynamic-provision.yaml) to trigger dynamic provisioning

    ```bash
    kubectl create -f example/pvc-dynamic-provision.yaml
    ```

2. Mount the PVC in a [pod](example/nginx-demo-pod-with-dynamic-vol.yaml) as shown in the following example

    ```bash
    kubectl create -f example/nginx-demo-pod-with-dynamic-vol.yaml
    ```

3. Wait for the pod to be in running state

    ```bash
    kubectl get po -w | grep 'nginx-dynamic-vol'
    ```

4. Once the pod is running, copy the [index file](example/index.html) to the deployed nginx pod

    ```bash
    kubectl cp example/index.html nginx-dynamic-vol:/usr/share/nginx/html/
    ```

5. Access the home page served by nginx pod from the command line

    ```bash
    curl http://$(kubectl get pods nginx-dynamic-vol -o yaml | grep 'podIP:' | awk '{print $2}'):80
    ```

  Above command should retrieve the Quobyte CSI welcome page (in raw html format).

### Use existing volumes

Quobyte CSI requires the volume UUID to be passed on to the PV as `VolumeHandle`  

* Quobyte-csi supports both volume name and UUID
  * **To use Volume Name** `VolumeHandle` should be of the format `<Tenant_Name/UUID>|<Volume_Name>`
   and `nodePublishSecretRef` with Quobyte API login credentials should be specified as shown in the
   example PV `example/pv-existing-vol.yaml`
  * **To use Volume UUID** `VolumeHandle` can be `|<Volume_UUID>`.

In order to use the pre-provisioned `test` volume belonging to the tenant `My Tenant`, user needs to create
 a PV with `volumeHandle: My Tenant|test` as shown in the [example PV](example/pv-existing-vol.yaml).

1. Edit [example/pv-existing-vol.yaml](example/pv-existing-vol.yaml) and point it to the the pre-provisioned volume in Quobyte
 storage through `volumeHandle`. Create the PV with pre-provisioned volume.

    ```bash
    kubectl create -f example/pv-existing-vol.yaml
    ```

2. Create a [PVC](example/pvc-existing-vol.yaml) that matches the storage requirements with the above PV (make sure both PV and PVC refer
 to the same storage class). The created PVC will automatically binds to the PV.

    ```bash
    kubectl create -f example/pvc-existing-vol.yaml
    ```

3. Create a [pod](example/nginx-demo-pod-with-existing-vol.yaml) referring the PVC as shown in the below example

    ```bash
    kubectl create -f example/nginx-demo-pod-with-existing-vol.yaml
    ```

4. Wait for the pod to be in running state

    ```bash
    kubectl get po -w | grep 'nginx-existing-vol'
    ```

5. Once the pod is running, copy the [index file](example/index.html) to the deployed nginx pod

    ```bash
    kubectl cp example/index.html nginx-existing-vol:/usr/share/nginx/html/
    ```

6. Access the home page served by nginx pod from the command line

    ```bash
    curl http://$(kubectl get pods nginx-existing-vol -o yaml | grep 'podIP:' | awk '{print $2}'):80
    ```

    The above command should retrieve the Quobyte CSI welcome page (in raw html format).

## Uninstall Quobyte CSI

1. Delete Quobyte CSI containers and corresponding RBAC

    List available helm charts

    ```bash
    helm list
    ```

    Delete intentend chart

    ```bash
    helm delete <Quobyte-CSI-chart-name>
    ```
