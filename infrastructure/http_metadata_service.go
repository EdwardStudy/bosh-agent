package infrastructure

import (
	"encoding/json"
	"fmt"
	"crypto/sha1"
	"io/ioutil"
	"net/http"
	"time"

	boshplat "github.com/cloudfoundry/bosh-agent/platform"
	boshsettings "github.com/cloudfoundry/bosh-agent/settings"
	bosherr "github.com/cloudfoundry/bosh-utils/errors"
	boshhttp "github.com/cloudfoundry/bosh-utils/http"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
)

type httpMetadataService struct {
	metadataHost    string
	metadataHeaders map[string]string
	userdataPath    string
	instanceIDPath  string
	sshKeysPath     string
	resolver        DNSResolver
	platform        boshplat.Platform
	logTag          string
	logger          boshlog.Logger
	retryDelay      time.Duration
}

func NewHTTPMetadataService(
	metadataHost string,
	metadataHeaders map[string]string,
	userdataPath string,
	instanceIDPath string,
	sshKeysPath string,
	resolver DNSResolver,
	platform boshplat.Platform,
	logger boshlog.Logger,
) DynamicMetadataService {
	return httpMetadataService{
		metadataHost:    metadataHost,
		metadataHeaders: metadataHeaders,
		userdataPath:    userdataPath,
		instanceIDPath:  instanceIDPath,
		sshKeysPath:     sshKeysPath,
		resolver:        resolver,
		platform:        platform,
		logTag:          "httpMetadataService",
		logger:          logger,
		retryDelay:      1 * time.Second,
	}
}

func NewHTTPMetadataServiceWithCustomRetryDelay(
	metadataHost string,
	metadataHeaders map[string]string,
	userdataPath string,
	instanceIDPath string,
	sshKeysPath string,
	resolver DNSResolver,
	platform boshplat.Platform,
	logger boshlog.Logger,
	retryDelay time.Duration,
) DynamicMetadataService {
	return httpMetadataService{
		metadataHost:    metadataHost,
		metadataHeaders: metadataHeaders,
		userdataPath:    userdataPath,
		instanceIDPath:  instanceIDPath,
		sshKeysPath:     sshKeysPath,
		resolver:        resolver,
		platform:        platform,
		logTag:          "httpMetadataService",
		logger:          logger,
		retryDelay:      retryDelay,
	}
}

func (ms httpMetadataService) Load() error {
	return nil
}

func (ms httpMetadataService) GetPublicKey() (string, error) {
	if ms.sshKeysPath == "" {
		return "", nil
	}

	err := ms.ensureMinimalNetworkSetup()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s%s", ms.metadataHost, ms.sshKeysPath)

	respBytes, err := ms.doGet(url)
	if err != nil {
		return "", bosherr.WrapErrorf(err, "Getting open ssh key from url %s", url)
	}

	return string(respBytes), nil
}

func (ms httpMetadataService) GetInstanceID() (string, error) {
	if ms.instanceIDPath == "" {
		return "", nil
	}

	err := ms.ensureMinimalNetworkSetup()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s%s", ms.metadataHost, ms.instanceIDPath)

	respBytes, err := ms.doGet(url)
	if err != nil {
		return "", bosherr.WrapErrorf(err, "Getting instance id from url %s", url)
	}

	return string(respBytes), nil
}

func (ms httpMetadataService) GetValueAtPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("Can not retrieve metadata value for empthy path")
	}

	err := ms.ensureMinimalNetworkSetup()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s%s", ms.metadataHost, path)

	respBytes, err := ms.doGet(url)
	if err != nil {
		return "", bosherr.WrapErrorf(err, "Getting value from url %s", url)
	}

	return string(respBytes), nil
}
func (ms httpMetadataService) GetServerName() (string, error) {
	userData, err := ms.getUserData()
	if err != nil {
		return "", bosherr.WrapError(err, "Getting user data")
	}

	serverName := userData.Server.Name

	if len(serverName) == 0 {
		return "", bosherr.Error("Empty server name")
	}

	return serverName, nil
}

func (ms httpMetadataService) GetRegistryEndpoint() (string, error) {
	userData, err := ms.getUserData()
	if err != nil {
		return "", bosherr.WrapError(err, "Getting user data")
	}

	endpoint := userData.Registry.Endpoint
	nameServers := userData.DNS.Nameserver

	if len(nameServers) > 0 {
		endpoint, err = ms.resolver.LookupHost(nameServers, endpoint)
		if err != nil {
			return "", bosherr.WrapError(err, "Resolving registry endpoint")
		}
	}

	return endpoint, nil
}

func (ms httpMetadataService) GetNetworks() (boshsettings.Networks, error) {
	userData, err := ms.getUserData()
	if err != nil {
		return nil, bosherr.WrapError(err, "Getting user data")
	}

	return userData.Networks, nil
}

func (ms httpMetadataService) IsAvailable() bool { return true }

func (ms httpMetadataService) getUserData() (UserDataContentsType, error) {
	var userData UserDataContentsType

	err := ms.ensureMinimalNetworkSetup()
	if err != nil {
		return userData, err
	}

	userDataURL := fmt.Sprintf("%s%s", ms.metadataHost, ms.userdataPath)

	respBytes, err := ms.doGet(userDataURL)
	if err != nil {
		return userData, bosherr.WrapErrorf(err, "Getting user data from url %s", userDataURL)
	}

	err = json.Unmarshal(respBytes, &userData)
	if err != nil {
		return userData, bosherr.WrapErrorf(err, "Unmarshalling user data '%s'", string(respBytes))
	}

	return userData, nil
}

func (ms httpMetadataService) ensureMinimalNetworkSetup() error {
	// We check for configuration presence instead of verifying
	// that network is reachable because we want to preserve
	// network configuration that was passed to agent.
	configuredInterfaces, err := ms.platform.GetConfiguredNetworkInterfaces()
	if err != nil {
		return bosherr.WrapError(err, "Getting configured network interfaces")
	}

	if len(configuredInterfaces) == 0 {
		ms.logger.Debug(ms.logTag, "No configured networks found, setting up DHCP network")
		err = ms.platform.SetupNetworking(boshsettings.Networks{
			"eth0": {
				Type: boshsettings.NetworkTypeDynamic,
			},
		})
		if err != nil {
			return bosherr.WrapError(err, "Setting up initial DHCP network")
		}
	}

	return nil
}

func (ms httpMetadataService) doGet(url string) ([]byte, error) {
	cachePath := fmt.Sprintf("/var/vcap/bosh/http-metadata-service-%x", sha1.Sum([]byte(url)))

	cachedRespBytes, err := ms.platform.GetFs().ReadFile(cachePath)
	if err == nil {
		return cachedRespBytes, nil
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range ms.metadataHeaders {
		req.Header.Add(key, value)
	}

	client := boshhttp.NewRetryClient(
		&http.Client{},
		10,
		ms.retryDelay,
		ms.logger,
	)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			ms.logger.Warn(ms.logTag, "Failed to close response body when getting user data: %s", err.Error())
		}
	}()

	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, bosherr.WrapError(err, "Reading user data response body")
	}

	err = ms.platform.GetFs().WriteFile(cachePath, respBytes)
	if err != nil {
		return nil, bosherr.WrapError(err, "Caching response body")
	}

	return respBytes, nil
}
