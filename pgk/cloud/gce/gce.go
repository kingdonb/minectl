package gce

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
	"time"

	"github.com/minectl/pgk/update"

	"github.com/minectl/pgk/automation"
	"github.com/minectl/pgk/common"
	minctlTemplate "github.com/minectl/pgk/template"
	"github.com/pkg/errors"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/oslogin/v1"
)

type Credentials struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	ClientId    string `json:"client_id"`
}

type GCE struct {
	client             *compute.Service
	user               *oslogin.Service
	projectID          string
	serviceAccountName string
	serviceAccountID   string
	zone               string
	tmpl               *minctlTemplate.Template
}

func NewGCE(keyfile, zone string) (*GCE, error) {

	file, err := ioutil.ReadFile(keyfile)
	if err != nil {
		return nil, err
	}
	var cred Credentials
	err = json.Unmarshal(file, &cred)
	if err != nil {
		return nil, err
	}
	computeService, err := compute.NewService(context.Background(), option.WithCredentialsJSON(file))
	if err != nil {
		return nil, err
	}

	userService, err := oslogin.NewService(context.Background(), option.WithCredentialsJSON(file))
	if err != nil {
		return nil, err
	}
	tmpl, err := minctlTemplate.NewTemplateBash("sdb")
	if err != nil {
		return nil, err
	}
	return &GCE{
		client:             computeService,
		projectID:          cred.ProjectID,
		user:               userService,
		serviceAccountName: cred.ClientEmail,
		serviceAccountID:   cred.ClientId,
		zone:               zone,
		tmpl:               tmpl,
	}, nil
}

func (g *GCE) CreateServer(args automation.ServerArgs) (*automation.RessourceResults, error) {
	imageURL := "projects/ubuntu-os-cloud/global/images/ubuntu-2004-focal-v20210720"

	pubKeyFile, err := ioutil.ReadFile(fmt.Sprintf("%s.pub", args.MinecraftServer.GetSSH()))
	if err != nil {
		return nil, err
	}

	_, err = g.user.Users.ImportSshPublicKey(fmt.Sprintf("users/%s", g.serviceAccountName), &oslogin.SshPublicKey{
		Key:                string(pubKeyFile),
		ExpirationTimeUsec: 0,
	}).Context(context.Background()).Do()
	if err != nil {
		return nil, err
	}

	diskInsertOp, err := g.client.Disks.Insert(g.projectID, args.MinecraftServer.GetRegion(), &compute.Disk{
		Name:   fmt.Sprintf("%s-vol", args.MinecraftServer.GetName()),
		SizeGb: int64(args.MinecraftServer.GetVolumeSize()),
		Type:   fmt.Sprintf("zones/%s/diskTypes/pd-standard", args.MinecraftServer.GetRegion()),
	}).Context(context.Background()).Do()
	if err != nil {
		return nil, err
	}

	stillCreating := true
	for stillCreating {
		diskInsertOps, err := g.client.ZoneOperations.Get(g.projectID, args.MinecraftServer.GetRegion(), diskInsertOp.Name).Context(context.Background()).Do()
		if err != nil {
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		if diskInsertOps.Status == "DONE" {
			stillCreating = false
		} else {
			time.Sleep(2 * time.Second)
		}
	}

	userData, err := g.tmpl.GetTemplate(args.MinecraftServer, minctlTemplate.TemplateBash)
	if err != nil {
		return nil, err
	}

	oslogin := "TRUE"
	autoRestart := true
	instance := &compute.Instance{
		Name:        args.MinecraftServer.GetName(),
		MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", args.MinecraftServer.GetRegion(), args.MinecraftServer.GetSize()),
		Disks: []*compute.AttachedDisk{
			{
				AutoDelete: true,
				Boot:       true,
				Type:       "PERSISTENT",
				DiskSizeGb: 10,
				InitializeParams: &compute.AttachedDiskInitializeParams{
					SourceImage: imageURL,
				},
			},
			{
				Source: fmt.Sprintf("zones/%s/disks/%s-vol", args.MinecraftServer.GetRegion(),
					args.MinecraftServer.GetName()),
			},
		},
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{
				{
					Key:   "enable-oslogin",
					Value: &oslogin,
				},
				{
					Key:   "startup-script",
					Value: &userData,
				},
			},
		},
		Scheduling: &compute.Scheduling{
			AutomaticRestart:  &autoRestart,
			OnHostMaintenance: "MIGRATE",
			Preemptible:       false,
		},
		NetworkInterfaces: []*compute.NetworkInterface{
			{
				AccessConfigs: []*compute.AccessConfig{
					{
						Type: "ONE_TO_ONE_NAT",
						Name: "External NAT",
					},
				},
				Network: "/global/networks/default",
			},
		},
		ServiceAccounts: []*compute.ServiceAccount{
			{
				Email: g.serviceAccountName,
				Scopes: []string{
					compute.DevstorageFullControlScope,
					compute.ComputeScope,
				},
			},
		},
		Labels: map[string]string{
			common.InstanceTag: "true",
		},
		Tags: &compute.Tags{
			Items: []string{common.InstanceTag, args.MinecraftServer.GetEdition()},
		},
	}

	insertInstanceOp, err := g.client.Instances.Insert(g.projectID, args.MinecraftServer.GetRegion(), instance).Context(context.Background()).Do()
	if err != nil {
		return nil, err
	}

	stillCreating = true
	for stillCreating {
		insertInstanceOp, err := g.client.ZoneOperations.Get(g.projectID, args.MinecraftServer.GetRegion(), insertInstanceOp.Name).Context(context.Background()).Do()
		if err != nil {
			return nil, err
		}
		if insertInstanceOp.Status == "DONE" {
			stillCreating = false
		} else {
			time.Sleep(2 * time.Second)
		}
	}

	firewallRule := &compute.Firewall{
		Name:        fmt.Sprintf("%s-fw", args.MinecraftServer.GetName()),
		Description: "Firewall rule created by minectl",
		Network:     fmt.Sprintf("projects/%s/global/networks/default", g.projectID),
		Allowed: []*compute.FirewallAllowed{
			{
				IPProtocol: "tcp",
			},
		},
		SourceRanges: []string{"0.0.0.0/0"},
		Direction:    "INGRESS",
		TargetTags:   []string{common.InstanceTag},
	}
	_, err = g.client.Firewalls.Insert(g.projectID, firewallRule).Context(context.Background()).Do()
	if err != nil {
		return nil, err
	}

	instanceListOp, err := g.client.Instances.List(g.projectID, args.MinecraftServer.GetRegion()).
		Filter(fmt.Sprintf("(name=%s)", args.MinecraftServer.GetName())).
		Context(context.Background()).
		Do()
	if err != nil {
		return nil, err
	}

	if len(instanceListOp.Items) == 1 {
		instance := instanceListOp.Items[0]
		ip := instance.NetworkInterfaces[0].AccessConfigs[0].NatIP
		return &automation.RessourceResults{
			ID:       strconv.Itoa(int(instance.Id)),
			Name:     instance.Name,
			Region:   instance.Zone,
			PublicIP: ip,
			Tags:     strings.Join(instance.Tags.Items, ","),
		}, err
	} else {
		return nil, errors.New("no instances created")
	}

}

func (g *GCE) DeleteServer(id string, args automation.ServerArgs) error {
	profileGetOp, err := g.user.Users.GetLoginProfile("users/minctl@minectl-fn.iam.gserviceaccount.com").Context(context.Background()).Do()
	if err != nil {
		return err
	}
	for _, posixAccount := range profileGetOp.PosixAccounts {
		_, err := g.user.Users.Projects.Delete(posixAccount.Name).Context(context.Background()).Do()
		if err != nil {
			return err
		}
	}
	for _, publicKey := range profileGetOp.SshPublicKeys {
		_, err = g.user.Users.SshPublicKeys.Delete(publicKey.Name).Context(context.Background()).Do()
		if err != nil {
			return err
		}
	}
	instancesListOp, err := g.client.Instances.List(g.projectID, args.MinecraftServer.GetRegion()).
		Filter(fmt.Sprintf("(id=%s)", id)).
		Context(context.Background()).
		Do()
	if err != nil {
		return err
	}
	if len(instancesListOp.Items) == 1 {
		instanceDeleteOp, err := g.client.Instances.Delete(g.projectID, args.MinecraftServer.GetRegion(), instancesListOp.Items[0].Name).
			Context(context.Background()).
			Do()
		if err != nil {
			return err
		}
		var stillDeleting = true
		for stillDeleting {
			instanceDeleteOp, err := g.client.ZoneOperations.Get(g.projectID, args.MinecraftServer.GetRegion(), instanceDeleteOp.Name).Context(context.Background()).Do()
			if err != nil {
				return err
			}
			if instanceDeleteOp.Status == "DONE" {
				stillDeleting = false
			} else {
				time.Sleep(2 * time.Second)
			}
		}

	}

	diskListOp, err := g.client.Disks.List(g.projectID, args.MinecraftServer.GetRegion()).
		Filter(fmt.Sprintf("(name=%s)", fmt.Sprintf("%s-vol", args.MinecraftServer.GetName()))).
		Context(context.Background()).
		Do()
	if err != nil {
		return err
	}
	for _, disk := range diskListOp.Items {
		_, err := g.client.Disks.Delete(g.projectID, args.MinecraftServer.GetRegion(), disk.Name).Context(context.Background()).Do()
		if err != nil {
			return err
		}
	}

	firewallListOps, err := g.client.Firewalls.List(g.projectID).Filter(fmt.Sprintf("(name=%s)", fmt.Sprintf("%s-fw", args.MinecraftServer.GetName()))).Context(context.Background()).Do()
	if err != nil {
		return err
	}
	for _, firewall := range firewallListOps.Items {
		_, err := g.client.Firewalls.Delete(g.projectID, firewall.Name).Context(context.Background()).Do()
		if err != nil {
			return err
		}
	}

	return nil
}

func (g *GCE) ListServer() ([]automation.RessourceResults, error) {
	instanceListOp, err := g.client.Instances.List(g.projectID, g.zone).
		Filter(fmt.Sprintf("(labels.%s=true)", common.InstanceTag)).
		Context(context.Background()).Do()
	if err != nil {
		return nil, err
	}
	var result []automation.RessourceResults
	for _, instance := range instanceListOp.Items {
		result = append(result, automation.RessourceResults{
			ID:       strconv.Itoa(int(instance.Id)),
			Name:     instance.Name,
			Region:   instance.Zone,
			PublicIP: instance.NetworkInterfaces[0].AccessConfigs[0].NatIP,
			Tags:     strings.Join(instance.Tags.Items, ","),
		})
	}
	return result, nil
}

func (g *GCE) UpdateServer(id string, args automation.ServerArgs) error {

	instancesListOp, err := g.client.Instances.List(g.projectID, args.MinecraftServer.GetRegion()).
		Filter(fmt.Sprintf("(id=%s)", id)).
		Context(context.Background()).
		Do()
	if err != nil {
		return err
	}
	if len(instancesListOp.Items) == 1 {
		instance := instancesListOp.Items[0]
		remoteCommand := update.NewRemoteServer(args.MinecraftServer.GetSSH(), instance.NetworkInterfaces[0].AccessConfigs[0].NatIP, fmt.Sprintf("sa_%s", g.serviceAccountID))
		err = remoteCommand.UpdateServer(args.MinecraftServer)
		if err != nil {
			return err
		}
	}

	return nil
}
