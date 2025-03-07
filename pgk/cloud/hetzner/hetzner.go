package hetzner

import (
	"context"
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
	"time"

	"github.com/minectl/pgk/update"

	"github.com/hetznercloud/hcloud-go/hcloud"
	"github.com/minectl/pgk/automation"
	"github.com/minectl/pgk/common"
	minctlTemplate "github.com/minectl/pgk/template"
)

type Hetzner struct {
	client *hcloud.Client
	tmpl   *minctlTemplate.Template
}

func NewHetzner(APIKey string) (*Hetzner, error) {

	client := hcloud.NewClient(hcloud.WithToken(APIKey))
	tmpl, err := minctlTemplate.NewTemplateCloudConfig("sdb")
	if err != nil {
		return nil, err
	}
	hetzner := &Hetzner{
		client: client,
		tmpl:   tmpl,
	}
	return hetzner, nil
}

func (h *Hetzner) CreateServer(args automation.ServerArgs) (*automation.RessourceResults, error) {
	pubKeyFile, err := ioutil.ReadFile(fmt.Sprintf("%s.pub", args.MinecraftServer.GetSSH()))
	if err != nil {
		return nil, err
	}
	key, _, err := h.client.SSHKey.Create(context.Background(), hcloud.SSHKeyCreateOpts{
		Name:      fmt.Sprintf("%s-ssh", args.MinecraftServer.GetName()),
		PublicKey: string(pubKeyFile),
	})
	if err != nil {
		return nil, err
	}

	location, _, err := h.client.Location.Get(context.Background(), args.MinecraftServer.GetRegion())
	if err != nil {
		return nil, err
	}

	volume, _, err := h.client.Volume.Create(context.Background(), hcloud.VolumeCreateOpts{
		Name:     fmt.Sprintf("%s-vol", args.MinecraftServer.GetName()),
		Size:     args.MinecraftServer.GetVolumeSize(),
		Location: location,
		Format:   hcloud.String("ext4"),
	})
	if err != nil {
		return nil, err
	}

	userData, err := h.tmpl.GetTemplate(args.MinecraftServer, minctlTemplate.TemplateCloudConfig)
	if err != nil {
		return nil, err
	}
	image, _, err := h.client.Image.GetByName(context.Background(), "ubuntu-20.04")
	if err != nil {
		return nil, err
	}

	plan, _, err := h.client.ServerType.GetByName(context.Background(), args.MinecraftServer.GetSize())
	if err != nil {
		return nil, err
	}
	serverCreateReq, _, err := h.client.Server.Create(context.Background(), hcloud.ServerCreateOpts{
		Name:       args.MinecraftServer.GetName(),
		ServerType: plan,
		Image:      image,
		Location:   location,
		SSHKeys:    []*hcloud.SSHKey{key},
		Volumes:    []*hcloud.Volume{volume.Volume},
		UserData:   userData,
		Labels:     map[string]string{common.InstanceTag: "true", args.MinecraftServer.GetEdition(): "true"},
		Automount:  hcloud.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	server := serverCreateReq.Server
	stillCreating := true

	for stillCreating {
		server, _, err := h.client.Server.GetByID(context.Background(), server.ID)
		if err != nil {
			return nil, err
		}
		if server.Status == "running" {
			stillCreating = false
		} else {
			time.Sleep(2 * time.Second)
		}
	}
	return &automation.RessourceResults{
		ID:       strconv.Itoa(server.ID),
		Name:     server.Name,
		Region:   server.Datacenter.Location.Name,
		PublicIP: server.PublicNet.IPv4.IP.String(),
		Tags:     hetznerLabelsToTags(server.Labels),
	}, err
}

func (h *Hetzner) DeleteServer(id string, args automation.ServerArgs) error {
	serverID, _ := strconv.Atoi(id)
	server, _, err := h.client.Server.GetByID(context.Background(), serverID)
	if err != nil {
		return err
	}
	volume, _, err := h.client.Volume.Get(context.Background(), fmt.Sprintf("%s-vol", args.MinecraftServer.GetName()))
	if err != nil {
		return err
	}

	res, _, err := h.client.Volume.Detach(context.Background(), volume)
	if err != nil {
		return err
	}
	stillDetatching := true
	for stillDetatching {
		action, _, err := h.client.Action.GetByID(context.Background(), res.ID)
		if err != nil {
			return err
		}
		if action.Status == "success" {
			stillDetatching = false
		} else {
			time.Sleep(2 * time.Second)
		}
	}
	_, err = h.client.Volume.Delete(context.Background(), volume)
	if err != nil {
		return err
	}

	_, err = h.client.Server.Delete(context.Background(), server)
	if err != nil {
		return err
	}

	key, _, err := h.client.SSHKey.Get(context.Background(), fmt.Sprintf("%s-ssh", args.MinecraftServer.GetName()))
	if err != nil {
		return err
	}
	_, err = h.client.SSHKey.Delete(context.Background(), key)
	if err != nil {
		return err
	}
	return nil
}

func hetznerLabelsToTags(label map[string]string) string {
	var tags []string
	for key := range label {
		tags = append(tags, key)
	}
	return strings.Join(tags, ",")
}

func (h *Hetzner) ListServer() ([]automation.RessourceResults, error) {
	servers, err := h.client.Server.All(context.Background())
	if err != nil {
		return nil, err
	}
	var result []automation.RessourceResults
	for _, server := range servers {
		for key := range server.Labels {
			if key == common.InstanceTag {
				result = append(result, automation.RessourceResults{
					ID:       strconv.Itoa(server.ID),
					Name:     server.Name,
					Region:   server.Datacenter.Location.Name,
					PublicIP: server.PublicNet.IPv4.IP.String(),
					Tags:     hetznerLabelsToTags(server.Labels),
				})
			}
		}
	}
	return result, nil
}

func (h *Hetzner) UpdateServer(id string, args automation.ServerArgs) error {
	intID, _ := strconv.Atoi(id)
	instance, _, err := h.client.Server.GetByID(context.Background(), intID)
	if err != nil {
		return err
	}

	remoteCommand := update.NewRemoteServer(args.MinecraftServer.GetSSH(), instance.PublicNet.IPv4.IP.String(), "root")
	err = remoteCommand.UpdateServer(args.MinecraftServer)
	if err != nil {
		return err
	}
	return nil
}
