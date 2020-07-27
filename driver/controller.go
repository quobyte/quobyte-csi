package driver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	quobyte "github.com/quobyte/api/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	//DefaultTenant Default Tenant to use if none provided by user
	DefaultTenant = "My Tenant"
	//DefaultConfig Default configuration to use if none provided by user
	DefaultConfig = "BASE"
	//DefaultCreateQuota Quobyte CSI by default does NOT create volumes with Quotas.
	// To create Quotas for the volumes, set createQuota: "true" in storage class
	DefaultCreateQuota = false
	DefaultUser        = "root"
	DefaultGroup       = "nfsnobody"
	DefaultAccessModes = 777
	// Metadata from K8S CSI external provisioner
	pvcNamespaceKey = "csi.storage.k8s.io/pvc/namespace"
)

// CreateVolume creates quobyte volume
func (d *QuobyteDriver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("container orchestrator should send the storage cluster details")
	}
	err := validateVolCapabilities(req.GetVolumeCapabilities())
	if err != nil {
		return nil, err
	}
	params := req.Parameters
	secrets := req.Secrets
	capacity := req.GetCapacityRange().RequiredBytes
	volName := req.Name
	volRequest := &quobyte.CreateVolumeRequest{}
	volRequest.Name = volName
	volRequest.TenantId = DefaultTenant
	volRequest.ConfigurationName = DefaultConfig
	volRequest.RootUserId = DefaultUser
	volRequest.RootGroupId = DefaultGroup
	createQuota := DefaultCreateQuota
	volRequest.AccessMode = DefaultAccessModes
	for k, v := range params {
		switch strings.ToLower(k) {
		case "quobytetenant":
			volRequest.TenantId = v
		case "user":
			volRequest.RootUserId = v
		case "group":
			volRequest.RootGroupId = v
		case "quobyteconfig":
			volRequest.ConfigurationName = v
		case "createquota":
			createQuota = strings.ToLower(v) == "true"
		case "labels":
			volRequest.Label, err = parseLabels(v)
			if err != nil {
				return nil, err
			}
		case "accessmode":
			u64, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return nil, err
			}
			volRequest.AccessMode = int32(u64)
		}
	}
	quobyteClient, err := getAPIClient(secrets, d.ApiURL)
	if err != nil {
		return nil, err
	}

	if d.UseK8SNamespaceAsQuobyteTenant {
		if pvcNamespace, ok := params[pvcNamespaceKey]; ok {
			volRequest.TenantId = pvcNamespace
		} else {
			return nil, fmt.Errorf("To use K8S namespace to Quobyte tenant mapping quay.io/k8scsi/csi-provisioner" +
				"should be deployed with --extra-create-metadata=true. Please redeploy driver with the above flag and retry.")
		}
	}

	volRequest.TenantId, err = quobyteClient.GetTenantUUID(volRequest.TenantId)
	if err != nil {
		return nil, err
	}

	volCreateResp, err := quobyteClient.CreateVolume(volRequest)
	var volUUID string
	if err != nil {
		// CSI requires idempotency. (calling volume create multiple times should return the volume if it already exists)
		if !strings.Contains(err.Error(), "ENTITY_EXISTS_ALREADY/POSIX_ERROR_NONE") {
			return nil, err
		}
		volUUID = getUUIDFromError(fmt.Sprintf("%v", err))
	} else {
		volUUID = volCreateResp.VolumeUuid
	}
	if createQuota {
		err := quobyteClient.SetVolumeQuota(volUUID, capacity)
		if err != nil {
			req := &quobyte.DeleteVolumeRequest{VolumeUuid: volUUID}
			quobyteClient.DeleteVolume(req)
			return nil, err
		}
	}
	resp := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			// CSI does not pass on vendor specific parameters to DeleteVolume and we require API url during volume delete
			// this hacky append serves the purpose as of now. The format of the hack <TenantName/TenantUUID>|<VOL_NAME/VOLUME_UUID>
			// Implications of this are
			// 	 1. All the subsequent calls should not use value of req.GetVolumeId() or req.VolumeId directly as volume name
			//   but parse and resolve UUID to name wherever required.
			//   2. Must be aware of the  <TenantName/TenantUUID>|<VOL_NAME/VOLUME_UUID> while using req.GetVolumeId() or req.VolumeId

			// Currently volume handle is the combination of  <TenantName/TenantUUID>, and <VOL_NAME/VOLUME_UUID>
			// due to the limitation of CSI not passing storage vendor specific parameters. Dynamic provision used UUID returned by
			// Quobyte's CreateVolume call as it does not require name to UUID resolution calls. But user can configure either name or UUID
			// for pre-provisioned volumes
			VolumeId:      volRequest.TenantId + "|" + volUUID,
			CapacityBytes: capacity,
		},
	}
	return resp, nil
}

// DeleteVolume deletes the given volume.
func (d *QuobyteDriver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volID := req.GetVolumeId()
	if len(volID) == 0 {
		return nil, fmt.Errorf("volumeId is required for DeleteVolume")
	}
	secrets := req.GetSecrets()
	params := strings.Split(volID, "|")
	if len(params) < 2 {
		return nil, fmt.Errorf("given volumeHandle '%s' is not in the form <Tenant_Name/Tenant_UUID>|<VOL_NAME/VOL_UUID>", volID)
	}
	quobyteClient, err := getAPIClient(secrets, d.ApiURL)
	if err != nil {
		return nil, err
	}
	err = quobyteClient.DeleteVolumeByResolvingNamesToUUID(params[1], params[0])
	if err != nil {
		return nil, err
	}
	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerPublishVolume Quobyte CSI does not implement this method. Quobyte Client is responsible for attaching volume.
func (d *QuobyteDriver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	// Quobyte client mounts the volume if it exists
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerGetVolume Quobyte CSI does not implement this method.
func (d *QuobyteDriver) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return &csi.ControllerGetVolumeResponse{}, nil
}

// ControllerUnpublishVolume Quobyte CSI does not implement this method.
func (d *QuobyteDriver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	// Quobyte does not require any clean up, return to the Quobyte client
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities Quobyte CSI does not implement this method.
func (d *QuobyteDriver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ValidateVolumeCapabilities: Not implented by Quobyte CSI")
}

// ListVolumes Quobyte CSI does not implement this method.
func (d *QuobyteDriver) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ListVolumes: Not implented by Quobyte CSI")
}

// GetCapacity Quobyte volumes are not capacity bound by default
func (d *QuobyteDriver) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	// TODO (venkat) : This seems to be the storage system capacity query and not of the volume
	return nil, status.Errorf(codes.Unimplemented, "GetCapacity: Quobyte  does not support it, at the moment.")
}

// ControllerGetCapabilities returns supported capabilities.
// CREATE_DELETE_VOLUME is required but
// PUBLISH_UNPUBLISH_VOLUME not required since Quobyte Client does the volume attachments to the node.
func (d *QuobyteDriver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	newCap := func(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
		return &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: cap,
				},
			},
		}
	}

	var caps []*csi.ControllerServiceCapability
	for _, cap := range []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		//	csi.ControllerServiceCapability_RPC_GET_CAPACITY,
	} {
		caps = append(caps, newCap(cap))
	}

	resp := &csi.ControllerGetCapabilitiesResponse{
		Capabilities: caps,
	}

	return resp, nil
}

func (d *QuobyteDriver) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "CreateSnapshot: Snapshots are not implemented by Quobyte CSI.")
}

func (d *QuobyteDriver) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "DeleteSnapshot: Snapshots are not implemented by Quobyte CSI.")
}

func (d *QuobyteDriver) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ListSnapshots: Snapshots are not implemented by Quobyte CSI.")
}

func (d *QuobyteDriver) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	capacity := req.CapacityRange.RequiredBytes
	d.expandVolume(&ExpandVolumeReq{volID: req.VolumeId, expandSecrets: req.Secrets, capacity: capacity})
	return &csi.ControllerExpandVolumeResponse{CapacityBytes: capacity}, nil
}
