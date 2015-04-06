package net_test

import (
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	boshlog "github.com/cloudfoundry/bosh-agent/logger"
	. "github.com/cloudfoundry/bosh-agent/platform/net"
	fakearp "github.com/cloudfoundry/bosh-agent/platform/net/arp/fakes"
	boship "github.com/cloudfoundry/bosh-agent/platform/net/ip"
	fakeip "github.com/cloudfoundry/bosh-agent/platform/net/ip/fakes"
	boshsettings "github.com/cloudfoundry/bosh-agent/settings"
	fakesys "github.com/cloudfoundry/bosh-agent/system/fakes"
)

var _ = Describe("centosNetManager", describeCentosNetManager)

func describeCentosNetManager() {
	var (
		fs                            *fakesys.FakeFileSystem
		cmdRunner                     *fakesys.FakeCmdRunner
		ipResolver                    *fakeip.FakeResolver
		addressBroadcaster            *fakearp.FakeAddressBroadcaster
		netManager                    Manager
		interfaceConfigurationCreator InterfaceConfigurationCreator
	)

	BeforeEach(func() {
		fs = fakesys.NewFakeFileSystem()
		cmdRunner = fakesys.NewFakeCmdRunner()
		ipResolver = &fakeip.FakeResolver{}
		logger := boshlog.NewLogger(boshlog.LevelNone)
		interfaceConfigurationCreator = NewInterfaceConfigurationCreator(logger)
		addressBroadcaster = &fakearp.FakeAddressBroadcaster{}
		netManager = NewCentosNetManager(
			fs,
			cmdRunner,
			ipResolver,
			interfaceConfigurationCreator,
			addressBroadcaster,
			logger,
		)
	})

	Describe("SetupNetworking", func() {
		var (
			dhcpNetwork                           boshsettings.Network
			staticNetwork                         boshsettings.Network
			expectedNetworkConfigurationForStatic string
			expectedNetworkConfigurationForDHCP   string
			expectedDhclientConfiguration         string
		)

		BeforeEach(func() {
			dhcpNetwork = boshsettings.Network{
				Type:    "dynamic",
				Default: []string{"dns"},
				DNS:     []string{"8.8.8.8", "9.9.9.9"},
				Mac:     "fake-dhcp-mac-address",
			}
			staticNetwork = boshsettings.Network{
				Type:    "manual",
				IP:      "1.2.3.4",
				Netmask: "255.255.255.0",
				Gateway: "3.4.5.6",
				Mac:     "fake-static-mac-address",
			}

			expectedNetworkConfigurationForStatic = `DEVICE=ethstatic
BOOTPROTO=static
IPADDR=1.2.3.4
NETMASK=255.255.255.0
BROADCAST=1.2.3.255
GATEWAY=3.4.5.6
ONBOOT=yes
NM_CONTROLLED=no
PEERDNS=no
DNS1=8.8.8.8
DNS2=9.9.9.9
`

			expectedNetworkConfigurationForDHCP = `DEVICE=ethdhcp
BOOTPROTO=dhcp
ONBOOT=yes
PEERDNS=yes
`

			expectedDhclientConfiguration = `# Generated by bosh-agent

option rfc3442-classless-static-routes code 121 = array of unsigned integer 8;

send host-name "<hostname>";

request subnet-mask, broadcast-address, time-offset, routers,
	domain-name, domain-name-servers, domain-search, host-name,
	netbios-name-servers, netbios-scope, interface-mtu,
	rfc3442-classless-static-routes, ntp-servers;

prepend domain-name-servers 8.8.8.8, 9.9.9.9;
`
		})

		writeNetworkDevice := func(iface string, macAddress string, isPhysical bool) string {
			interfacePath := fmt.Sprintf("/sys/class/net/%s", iface)
			fs.WriteFile(interfacePath, []byte{})
			if isPhysical {
				fs.WriteFile(fmt.Sprintf("/sys/class/net/%s/device", iface), []byte{})
			}
			fs.WriteFileString(fmt.Sprintf("/sys/class/net/%s/address", iface), fmt.Sprintf("%s\n", macAddress))

			return interfacePath
		}

		stubInterfacesWithVirtual := func(physicalInterfaces map[string]boshsettings.Network, virtualInterfaces []string) {
			interfacePaths := []string{}

			for iface, networkSettings := range physicalInterfaces {
				interfacePaths = append(interfacePaths, writeNetworkDevice(iface, networkSettings.Mac, true))
			}

			for _, iface := range virtualInterfaces {
				interfacePaths = append(interfacePaths, writeNetworkDevice(iface, "virtual", false))
			}

			fs.SetGlob("/sys/class/net/*", interfacePaths)
		}

		stubInterfaces := func(physicalInterfaces map[string]boshsettings.Network) {
			stubInterfacesWithVirtual(physicalInterfaces, nil)
		}

		It("writes a network script for static and dynamic interfaces", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).ToNot(HaveOccurred())

			staticConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethstatic")
			Expect(staticConfig).ToNot(BeNil())
			Expect(staticConfig.StringContents()).To(Equal(expectedNetworkConfigurationForStatic))

			dhcpConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethdhcp")
			Expect(dhcpConfig).ToNot(BeNil())
			Expect(dhcpConfig.StringContents()).To(Equal(expectedNetworkConfigurationForDHCP))
		})

		It("returns errors from glob /sys/class/net/", func() {
			fs.GlobErr = errors.New("fs-glob-error")
			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fs-glob-error"))
		})

		It("returns errors from writing the network configuration", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"dhcp":   dhcpNetwork,
				"static": staticNetwork,
			})
			fs.WriteFileError = errors.New("fs-write-file-error")
			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fs-write-file-error"))
		})

		It("returns errors when it can't create network interface configurations", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethstatic": staticNetwork,
			})

			staticNetwork.Netmask = "not an ip" //will cause InterfaceConfigurationCreator to fail
			err := netManager.SetupNetworking(boshsettings.Networks{"static-network": staticNetwork}, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Creating interface configurations"))
		})

		It("wrtites a dhcp configuration if there are dhcp networks", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).ToNot(HaveOccurred())

			dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
			Expect(dhcpConfig).ToNot(BeNil())
			Expect(dhcpConfig.StringContents()).To(Equal(expectedDhclientConfiguration))

			dhcpConfigSymlink := fs.GetFileTestStat("/etc/dhcp/dhclient-ethdhcp.conf")
			Expect(dhcpConfigSymlink).ToNot(BeNil())
			Expect(dhcpConfigSymlink.SymlinkTarget).To(Equal("/etc/dhcp/dhclient.conf"))
		})

		It("writes a dhcp configuration without prepended dns servers if there are no dns servers specified", func() {
			dhcpNetworkWithoutDNS := boshsettings.Network{
				Type: "dynamic",
				Mac:  "fake-dhcp-mac-address",
			}

			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp": dhcpNetwork,
			})

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetworkWithoutDNS}, nil)
			Expect(err).ToNot(HaveOccurred())

			dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
			Expect(dhcpConfig).ToNot(BeNil())
			Expect(dhcpConfig.StringContents()).To(Equal(`# Generated by bosh-agent

option rfc3442-classless-static-routes code 121 = array of unsigned integer 8;

send host-name "<hostname>";

request subnet-mask, broadcast-address, time-offset, routers,
	domain-name, domain-name-servers, domain-search, host-name,
	netbios-name-servers, netbios-scope, interface-mtu,
	rfc3442-classless-static-routes, ntp-servers;

`))
			dhcpConfigSymlink := fs.GetFileTestStat("/etc/dhcp/dhclient-ethdhcp.conf")
			Expect(dhcpConfigSymlink).ToNot(BeNil())
			Expect(dhcpConfigSymlink.SymlinkTarget).To(Equal("/etc/dhcp/dhclient.conf"))
		})

		It("returns an error if it can't write a dhcp configuration", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			fs.WriteFileErrors["/etc/dhcp/dhclient.conf"] = errors.New("dhclient.conf-write-error")

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("dhclient.conf-write-error"))
		})

		It("returns an error if it can't symlink a dhcp configuration", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			fs.SymlinkError = errors.New("dhclient-ethdhcp.conf-symlink-error")

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("dhclient-ethdhcp.conf-symlink-error"))
		})

		It("doesn't write a dhcp configuration if there are no dhcp networks", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethstatic": staticNetwork,
			})

			err := netManager.SetupNetworking(boshsettings.Networks{"static-network": staticNetwork}, nil)
			Expect(err).ToNot(HaveOccurred())

			dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient-ethdhcp.conf")
			Expect(dhcpConfig).To(BeNil())
		})

		It("restarts the networks if any ifconfig file changes", func() {
			changingStaticNetwork := boshsettings.Network{
				Type:    "manual",
				IP:      "1.2.3.5",
				Netmask: "255.255.255.0",
				Gateway: "3.4.5.6",
				Mac:     "ethstatict-that-changes",
			}

			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":                dhcpNetwork,
				"ethstatic-that-changes": changingStaticNetwork,
				"ethstatic":              staticNetwork,
			})

			fs.WriteFileString("/etc/sysconfig/network-scripts/ifcfg-ethstatic", expectedNetworkConfigurationForStatic)
			fs.WriteFileString("/etc/dhcp/dhclient.conf", expectedDhclientConfiguration)

			err := netManager.SetupNetworking(boshsettings.Networks{
				"dhcp-network":            dhcpNetwork,
				"changing-static-network": changingStaticNetwork,
				"static-network":          staticNetwork,
			},
				nil)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(cmdRunner.RunCommands)).To(Equal(1))
			Expect(cmdRunner.RunCommands[0]).To(Equal([]string{"service", "network", "restart"}))
		})

		It("doesn't restart the networks if ifcfg and /etc/dhcp/dhclient.conf don't change", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			fs.WriteFileString("/etc/sysconfig/network-scripts/ifcfg-ethstatic", expectedNetworkConfigurationForStatic)
			fs.WriteFileString("/etc/sysconfig/network-scripts/ifcfg-ethdhcp", expectedNetworkConfigurationForDHCP)
			fs.WriteFileString("/etc/dhcp/dhclient.conf", expectedDhclientConfiguration)

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).ToNot(HaveOccurred())

			networkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethstatic")
			Expect(networkConfig).ToNot(BeNil())
			Expect(networkConfig.StringContents()).To(Equal(expectedNetworkConfigurationForStatic))

			dhcpConfig := fs.GetFileTestStat("/etc/dhcp/dhclient.conf")
			Expect(dhcpConfig.StringContents()).To(Equal(expectedDhclientConfiguration))

			Expect(len(cmdRunner.RunCommands)).To(Equal(0))
		})

		It("restarts the networks if /etc/dhcp/dhclient.conf changes", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			fs.WriteFileString("/etc/sysconfig/network-scripts/ifcfg-ethstatic", expectedNetworkConfigurationForStatic)

			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, nil)
			Expect(err).ToNot(HaveOccurred())

			networkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethstatic")
			Expect(networkConfig).ToNot(BeNil())
			Expect(networkConfig.StringContents()).To(Equal(expectedNetworkConfigurationForStatic))

			Expect(len(cmdRunner.RunCommands)).To(Equal(1))
			Expect(cmdRunner.RunCommands[0]).To(Equal([]string{"service", "network", "restart"}))
		})

		It("broadcasts MAC addresses for all interfaces", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			errCh := make(chan error)
			err := netManager.SetupNetworking(boshsettings.Networks{"dhcp-network": dhcpNetwork, "static-network": staticNetwork}, errCh)
			Expect(err).ToNot(HaveOccurred())

			broadcastErr := <-errCh // wait for all arpings
			Expect(broadcastErr).ToNot(HaveOccurred())

			Expect(addressBroadcaster.BroadcastMACAddressesAddresses).To(Equal([]boship.InterfaceAddress{
				boship.NewSimpleInterfaceAddress("ethstatic", "1.2.3.4"),
				boship.NewResolvingInterfaceAddress("ethdhcp", ipResolver),
			}))

		})

		It("skips vip networks", func() {
			stubInterfaces(map[string]boshsettings.Network{
				"ethdhcp":   dhcpNetwork,
				"ethstatic": staticNetwork,
			})

			vipNetwork := boshsettings.Network{
				Type:    "vip",
				Default: []string{"dns"},
				DNS:     []string{"8.8.8.8", "9.9.9.9"},
				Mac:     "fake-vip-mac-address",
				IP:      "9.8.7.6",
			}

			err := netManager.SetupNetworking(boshsettings.Networks{
				"dhcp-network":   dhcpNetwork,
				"static-network": staticNetwork,
				"vip-network":    vipNetwork,
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			networkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethstatic")
			Expect(networkConfig).ToNot(BeNil())
			Expect(networkConfig.StringContents()).To(Equal(expectedNetworkConfigurationForStatic))
		})

		Context("when no MAC address is provided in the settings", func() {
			var staticNetworkWithoutMAC boshsettings.Network

			BeforeEach(func() {
				staticNetworkWithoutMAC = boshsettings.Network{
					Type:    "manual",
					IP:      "1.2.3.4",
					Netmask: "255.255.255.0",
					Gateway: "3.4.5.6",
					DNS:     []string{"8.8.8.8", "9.9.9.9"},
					Default: []string{"dns"},
				}
			})

			It("configures network for single device", func() {
				stubInterfaces(
					map[string]boshsettings.Network{
						"ethstatic": staticNetwork,
					},
				)

				err := netManager.SetupNetworking(boshsettings.Networks{
					"static-network": staticNetworkWithoutMAC,
				}, nil)
				Expect(err).ToNot(HaveOccurred())

				networkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethstatic")
				Expect(networkConfig).ToNot(BeNil())
				Expect(networkConfig.StringContents()).To(Equal(expectedNetworkConfigurationForStatic))
			})

			It("configures network for single device, when a virtual device is also present", func() {
				stubInterfacesWithVirtual(
					map[string]boshsettings.Network{
						"ethstatic": staticNetwork,
					},
					[]string{"virtual"},
				)

				err := netManager.SetupNetworking(boshsettings.Networks{
					"static-network": staticNetworkWithoutMAC,
				}, nil)
				Expect(err).ToNot(HaveOccurred())

				physicalNetworkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-ethstatic")
				Expect(physicalNetworkConfig).ToNot(BeNil())
				Expect(physicalNetworkConfig.StringContents()).To(Equal(expectedNetworkConfigurationForStatic))

				virtualNetworkConfig := fs.GetFileTestStat("/etc/sysconfig/network-scripts/ifcfg-virtual")
				Expect(virtualNetworkConfig).To(BeNil())
			})
		})
	})
}
