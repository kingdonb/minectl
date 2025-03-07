package cloud

//CloudProvider
var cloudProvider = map[string]string{
	"do":       "DigitalOcean",
	"civo":     "Civo",
	"scaleway": "Scaleway",
	"hetzner":  "Hetzner",
	"linode":   "Linode",
	"ovh":      "OVHcloud",
	"equinix":  "Equinix Metal",
	"gce":      "Google Compute Engine",
}

func GetCloudProviderFullName(cloud string) string {
	return cloudProvider[cloud]
}
