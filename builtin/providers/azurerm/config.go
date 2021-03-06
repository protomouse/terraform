package azurerm

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"

	"github.com/Azure/azure-sdk-for-go/arm/cdn"
	"github.com/Azure/azure-sdk-for-go/arm/compute"
	"github.com/Azure/azure-sdk-for-go/arm/containerregistry"
	"github.com/Azure/azure-sdk-for-go/arm/eventhub"
	"github.com/Azure/azure-sdk-for-go/arm/keyvault"
	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/azure-sdk-for-go/arm/redis"
	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	"github.com/Azure/azure-sdk-for-go/arm/scheduler"
	"github.com/Azure/azure-sdk-for-go/arm/servicebus"
	"github.com/Azure/azure-sdk-for-go/arm/storage"
	"github.com/Azure/azure-sdk-for-go/arm/trafficmanager"
	mainStorage "github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/hashicorp/terraform/terraform"
	riviera "github.com/jen20/riviera/azure"
)

// ArmClient contains the handles to all the specific Azure Resource Manager
// resource classes' respective clients.
type ArmClient struct {
	clientId       string
	tenantId       string
	subscriptionId string
	environment    azure.Environment

	StopContext context.Context

	rivieraClient *riviera.Client

	availSetClient         compute.AvailabilitySetsClient
	usageOpsClient         compute.UsageOperationsClient
	vmExtensionImageClient compute.VirtualMachineExtensionImagesClient
	vmExtensionClient      compute.VirtualMachineExtensionsClient
	vmScaleSetClient       compute.VirtualMachineScaleSetsClient
	vmImageClient          compute.VirtualMachineImagesClient
	vmClient               compute.VirtualMachinesClient

	appGatewayClient             network.ApplicationGatewaysClient
	ifaceClient                  network.InterfacesClient
	loadBalancerClient           network.LoadBalancersClient
	localNetConnClient           network.LocalNetworkGatewaysClient
	publicIPClient               network.PublicIPAddressesClient
	secGroupClient               network.SecurityGroupsClient
	secRuleClient                network.SecurityRulesClient
	subnetClient                 network.SubnetsClient
	netUsageClient               network.UsagesClient
	vnetGatewayConnectionsClient network.VirtualNetworkGatewayConnectionsClient
	vnetGatewayClient            network.VirtualNetworkGatewaysClient
	vnetClient                   network.VirtualNetworksClient
	vnetPeeringsClient           network.VirtualNetworkPeeringsClient
	routeTablesClient            network.RouteTablesClient
	routesClient                 network.RoutesClient

	cdnProfilesClient  cdn.ProfilesClient
	cdnEndpointsClient cdn.EndpointsClient

	containerRegistryClient containerregistry.RegistriesClient

	eventHubClient              eventhub.EventHubsClient
	eventHubConsumerGroupClient eventhub.ConsumerGroupsClient
	eventHubNamespacesClient    eventhub.NamespacesClient

	providers           resources.ProvidersClient
	resourceGroupClient resources.GroupsClient
	tagsClient          resources.TagsClient
	resourceFindClient  resources.Client

	jobsClient            scheduler.JobsClient
	jobsCollectionsClient scheduler.JobCollectionsClient

	storageServiceClient storage.AccountsClient
	storageUsageClient   storage.UsageOperationsClient

	deploymentsClient resources.DeploymentsClient

	redisClient redis.Client

	trafficManagerProfilesClient  trafficmanager.ProfilesClient
	trafficManagerEndpointsClient trafficmanager.EndpointsClient

	serviceBusNamespacesClient    servicebus.NamespacesClient
	serviceBusTopicsClient        servicebus.TopicsClient
	serviceBusSubscriptionsClient servicebus.SubscriptionsClient

	keyVaultClient keyvault.VaultsClient
}

func withRequestLogging() autorest.SendDecorator {
	return func(s autorest.Sender) autorest.Sender {
		return autorest.SenderFunc(func(r *http.Request) (*http.Response, error) {
			// dump request to wire format
			if dump, err := httputil.DumpRequestOut(r, true); err == nil {
				log.Printf("[DEBUG] AzureRM Request: \n%s\n", dump)
			} else {
				// fallback to basic message
				log.Printf("[DEBUG] AzureRM Request: %s to %s\n", r.Method, r.URL)
			}

			resp, err := s.Do(r)
			if resp != nil {
				// dump response to wire format
				if dump, err := httputil.DumpResponse(resp, true); err == nil {
					log.Printf("[DEBUG] AzureRM Response for %s: \n%s\n", r.URL, dump)
				} else {
					// fallback to basic message
					log.Printf("[DEBUG] AzureRM Response: %s for %s\n", resp.Status, r.URL)
				}
			} else {
				log.Printf("[DEBUG] Request to %s completed with no response", r.URL)
			}
			return resp, err
		})
	}
}

func setUserAgent(client *autorest.Client) {
	version := terraform.VersionString()
	client.UserAgent = fmt.Sprintf("HashiCorp-Terraform-v%s", version)
}

// getArmClient is a helper method which returns a fully instantiated
// *ArmClient based on the Config's current settings.
func (c *Config) getArmClient() (*ArmClient, error) {
	// detect cloud from environment
	env, envErr := azure.EnvironmentFromName(c.Environment)
	if envErr != nil {
		// try again with wrapped value to support readable values like german instead of AZUREGERMANCLOUD
		wrapped := fmt.Sprintf("AZURE%sCLOUD", c.Environment)
		var innerErr error
		if env, innerErr = azure.EnvironmentFromName(wrapped); innerErr != nil {
			return nil, envErr
		}
	}

	// client declarations:
	client := ArmClient{
		clientId:       c.ClientID,
		tenantId:       c.TenantID,
		subscriptionId: c.SubscriptionID,
		environment:    env,
	}

	rivieraClient, err := riviera.NewClient(&riviera.AzureResourceManagerCredentials{
		ClientID:                c.ClientID,
		ClientSecret:            c.ClientSecret,
		TenantID:                c.TenantID,
		SubscriptionID:          c.SubscriptionID,
		ResourceManagerEndpoint: env.ResourceManagerEndpoint,
		ActiveDirectoryEndpoint: env.ActiveDirectoryEndpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("Error creating Riviera client: %s", err)
	}
	client.rivieraClient = rivieraClient

	oauthConfig, err := env.OAuthConfigForTenant(c.TenantID)
	if err != nil {
		return nil, err
	}

	// OAuthConfigForTenant returns a pointer, which can be nil.
	if oauthConfig == nil {
		return nil, fmt.Errorf("Unable to configure OAuthConfig for tenant %s", c.TenantID)
	}

	spt, err := azure.NewServicePrincipalToken(*oauthConfig, c.ClientID, c.ClientSecret, env.ResourceManagerEndpoint)
	if err != nil {
		return nil, err
	}

	endpoint := env.ResourceManagerEndpoint

	// NOTE: these declarations should be left separate for clarity should the
	// clients be wished to be configured with custom Responders/PollingModess etc...
	asc := compute.NewAvailabilitySetsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&asc.Client)
	asc.Authorizer = spt
	asc.Sender = autorest.CreateSender(withRequestLogging())
	client.availSetClient = asc

	uoc := compute.NewUsageOperationsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&uoc.Client)
	uoc.Authorizer = spt
	uoc.Sender = autorest.CreateSender(withRequestLogging())
	client.usageOpsClient = uoc

	vmeic := compute.NewVirtualMachineExtensionImagesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vmeic.Client)
	vmeic.Authorizer = spt
	vmeic.Sender = autorest.CreateSender(withRequestLogging())
	client.vmExtensionImageClient = vmeic

	vmec := compute.NewVirtualMachineExtensionsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vmec.Client)
	vmec.Authorizer = spt
	vmec.Sender = autorest.CreateSender(withRequestLogging())
	client.vmExtensionClient = vmec

	vmic := compute.NewVirtualMachineImagesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vmic.Client)
	vmic.Authorizer = spt
	vmic.Sender = autorest.CreateSender(withRequestLogging())
	client.vmImageClient = vmic

	vmssc := compute.NewVirtualMachineScaleSetsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vmssc.Client)
	vmssc.Authorizer = spt
	vmssc.Sender = autorest.CreateSender(withRequestLogging())
	client.vmScaleSetClient = vmssc

	vmc := compute.NewVirtualMachinesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vmc.Client)
	vmc.Authorizer = spt
	vmc.Sender = autorest.CreateSender(withRequestLogging())
	client.vmClient = vmc

	agc := network.NewApplicationGatewaysClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&agc.Client)
	agc.Authorizer = spt
	agc.Sender = autorest.CreateSender(withRequestLogging())
	client.appGatewayClient = agc

	crc := containerregistry.NewRegistriesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&crc.Client)
	crc.Authorizer = spt
	crc.Sender = autorest.CreateSender(withRequestLogging())
	client.containerRegistryClient = crc

	ehc := eventhub.NewEventHubsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&ehc.Client)
	ehc.Authorizer = spt
	ehc.Sender = autorest.CreateSender(withRequestLogging())
	client.eventHubClient = ehc

	chcgc := eventhub.NewConsumerGroupsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&chcgc.Client)
	chcgc.Authorizer = spt
	chcgc.Sender = autorest.CreateSender(withRequestLogging())
	client.eventHubConsumerGroupClient = chcgc

	ehnc := eventhub.NewNamespacesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&ehnc.Client)
	ehnc.Authorizer = spt
	ehnc.Sender = autorest.CreateSender(withRequestLogging())
	client.eventHubNamespacesClient = ehnc

	ifc := network.NewInterfacesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&ifc.Client)
	ifc.Authorizer = spt
	ifc.Sender = autorest.CreateSender(withRequestLogging())
	client.ifaceClient = ifc

	lbc := network.NewLoadBalancersClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&lbc.Client)
	lbc.Authorizer = spt
	lbc.Sender = autorest.CreateSender(withRequestLogging())
	client.loadBalancerClient = lbc

	lgc := network.NewLocalNetworkGatewaysClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&lgc.Client)
	lgc.Authorizer = spt
	lgc.Sender = autorest.CreateSender(withRequestLogging())
	client.localNetConnClient = lgc

	pipc := network.NewPublicIPAddressesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&pipc.Client)
	pipc.Authorizer = spt
	pipc.Sender = autorest.CreateSender(withRequestLogging())
	client.publicIPClient = pipc

	sgc := network.NewSecurityGroupsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&sgc.Client)
	sgc.Authorizer = spt
	sgc.Sender = autorest.CreateSender(withRequestLogging())
	client.secGroupClient = sgc

	src := network.NewSecurityRulesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&src.Client)
	src.Authorizer = spt
	src.Sender = autorest.CreateSender(withRequestLogging())
	client.secRuleClient = src

	snc := network.NewSubnetsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&snc.Client)
	snc.Authorizer = spt
	snc.Sender = autorest.CreateSender(withRequestLogging())
	client.subnetClient = snc

	vgcc := network.NewVirtualNetworkGatewayConnectionsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vgcc.Client)
	vgcc.Authorizer = spt
	vgcc.Sender = autorest.CreateSender(withRequestLogging())
	client.vnetGatewayConnectionsClient = vgcc

	vgc := network.NewVirtualNetworkGatewaysClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vgc.Client)
	vgc.Authorizer = spt
	vgc.Sender = autorest.CreateSender(withRequestLogging())
	client.vnetGatewayClient = vgc

	vnc := network.NewVirtualNetworksClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vnc.Client)
	vnc.Authorizer = spt
	vnc.Sender = autorest.CreateSender(withRequestLogging())
	client.vnetClient = vnc

	vnpc := network.NewVirtualNetworkPeeringsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&vnpc.Client)
	vnpc.Authorizer = spt
	vnpc.Sender = autorest.CreateSender(withRequestLogging())
	client.vnetPeeringsClient = vnpc

	rtc := network.NewRouteTablesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&rtc.Client)
	rtc.Authorizer = spt
	rtc.Sender = autorest.CreateSender(withRequestLogging())
	client.routeTablesClient = rtc

	rc := network.NewRoutesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&rc.Client)
	rc.Authorizer = spt
	rc.Sender = autorest.CreateSender(withRequestLogging())
	client.routesClient = rc

	rgc := resources.NewGroupsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&rgc.Client)
	rgc.Authorizer = spt
	rgc.Sender = autorest.CreateSender(withRequestLogging())
	client.resourceGroupClient = rgc

	pc := resources.NewProvidersClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&pc.Client)
	pc.Authorizer = spt
	pc.Sender = autorest.CreateSender(withRequestLogging())
	client.providers = pc

	tc := resources.NewTagsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&tc.Client)
	tc.Authorizer = spt
	tc.Sender = autorest.CreateSender(withRequestLogging())
	client.tagsClient = tc

	rf := resources.NewClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&rf.Client)
	rf.Authorizer = spt
	rf.Sender = autorest.CreateSender(withRequestLogging())
	client.resourceFindClient = rf

	jc := scheduler.NewJobsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&jc.Client)
	jc.Authorizer = spt
	jc.Sender = autorest.CreateSender(withRequestLogging())
	client.jobsClient = jc

	jcc := scheduler.NewJobCollectionsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&jcc.Client)
	jcc.Authorizer = spt
	jcc.Sender = autorest.CreateSender(withRequestLogging())
	client.jobsCollectionsClient = jcc

	ssc := storage.NewAccountsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&ssc.Client)
	ssc.Authorizer = spt
	ssc.Sender = autorest.CreateSender(withRequestLogging())
	client.storageServiceClient = ssc

	suc := storage.NewUsageOperationsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&suc.Client)
	suc.Authorizer = spt
	suc.Sender = autorest.CreateSender(withRequestLogging())
	client.storageUsageClient = suc

	cpc := cdn.NewProfilesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&cpc.Client)
	cpc.Authorizer = spt
	cpc.Sender = autorest.CreateSender(withRequestLogging())
	client.cdnProfilesClient = cpc

	cec := cdn.NewEndpointsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&cec.Client)
	cec.Authorizer = spt
	cec.Sender = autorest.CreateSender(withRequestLogging())
	client.cdnEndpointsClient = cec

	dc := resources.NewDeploymentsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&dc.Client)
	dc.Authorizer = spt
	dc.Sender = autorest.CreateSender(withRequestLogging())
	client.deploymentsClient = dc

	tmpc := trafficmanager.NewProfilesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&tmpc.Client)
	tmpc.Authorizer = spt
	tmpc.Sender = autorest.CreateSender(withRequestLogging())
	client.trafficManagerProfilesClient = tmpc

	tmec := trafficmanager.NewEndpointsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&tmec.Client)
	tmec.Authorizer = spt
	tmec.Sender = autorest.CreateSender(withRequestLogging())
	client.trafficManagerEndpointsClient = tmec

	rdc := redis.NewClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&rdc.Client)
	rdc.Authorizer = spt
	rdc.Sender = autorest.CreateSender(withRequestLogging())
	client.redisClient = rdc

	sbnc := servicebus.NewNamespacesClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&sbnc.Client)
	sbnc.Authorizer = spt
	sbnc.Sender = autorest.CreateSender(withRequestLogging())
	client.serviceBusNamespacesClient = sbnc

	sbtc := servicebus.NewTopicsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&sbtc.Client)
	sbtc.Authorizer = spt
	sbtc.Sender = autorest.CreateSender(withRequestLogging())
	client.serviceBusTopicsClient = sbtc

	sbsc := servicebus.NewSubscriptionsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&sbsc.Client)
	sbsc.Authorizer = spt
	sbsc.Sender = autorest.CreateSender(withRequestLogging())
	client.serviceBusSubscriptionsClient = sbsc

	kvc := keyvault.NewVaultsClientWithBaseURI(endpoint, c.SubscriptionID)
	setUserAgent(&kvc.Client)
	kvc.Authorizer = spt
	kvc.Sender = autorest.CreateSender(withRequestLogging())
	client.keyVaultClient = kvc

	return &client, nil
}

func (armClient *ArmClient) getKeyForStorageAccount(resourceGroupName, storageAccountName string) (string, bool, error) {
	accountKeys, err := armClient.storageServiceClient.ListKeys(resourceGroupName, storageAccountName)
	if accountKeys.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if err != nil {
		// We assume this is a transient error rather than a 404 (which is caught above),  so assume the
		// account still exists.
		return "", true, fmt.Errorf("Error retrieving keys for storage account %q: %s", storageAccountName, err)
	}

	if accountKeys.Keys == nil {
		return "", false, fmt.Errorf("Nil key returned for storage account %q", storageAccountName)
	}

	keys := *accountKeys.Keys
	return *keys[0].Value, true, nil
}

func (armClient *ArmClient) getBlobStorageClientForStorageAccount(resourceGroupName, storageAccountName string) (*mainStorage.BlobStorageClient, bool, error) {
	key, accountExists, err := armClient.getKeyForStorageAccount(resourceGroupName, storageAccountName)
	if err != nil {
		return nil, accountExists, err
	}
	if accountExists == false {
		return nil, false, nil
	}

	storageClient, err := mainStorage.NewClient(storageAccountName, key, armClient.environment.StorageEndpointSuffix,
		mainStorage.DefaultAPIVersion, true)
	if err != nil {
		return nil, true, fmt.Errorf("Error creating storage client for storage account %q: %s", storageAccountName, err)
	}

	blobClient := storageClient.GetBlobService()
	return &blobClient, true, nil
}

func (armClient *ArmClient) getFileServiceClientForStorageAccount(resourceGroupName, storageAccountName string) (*mainStorage.FileServiceClient, bool, error) {
	key, accountExists, err := armClient.getKeyForStorageAccount(resourceGroupName, storageAccountName)
	if err != nil {
		return nil, accountExists, err
	}
	if accountExists == false {
		return nil, false, nil
	}

	storageClient, err := mainStorage.NewClient(storageAccountName, key, armClient.environment.StorageEndpointSuffix,
		mainStorage.DefaultAPIVersion, true)
	if err != nil {
		return nil, true, fmt.Errorf("Error creating storage client for storage account %q: %s", storageAccountName, err)
	}

	fileClient := storageClient.GetFileService()
	return &fileClient, true, nil
}

func (armClient *ArmClient) getTableServiceClientForStorageAccount(resourceGroupName, storageAccountName string) (*mainStorage.TableServiceClient, bool, error) {
	key, accountExists, err := armClient.getKeyForStorageAccount(resourceGroupName, storageAccountName)
	if err != nil {
		return nil, accountExists, err
	}
	if accountExists == false {
		return nil, false, nil
	}

	storageClient, err := mainStorage.NewClient(storageAccountName, key, armClient.environment.StorageEndpointSuffix,
		mainStorage.DefaultAPIVersion, true)
	if err != nil {
		return nil, true, fmt.Errorf("Error creating storage client for storage account %q: %s", storageAccountName, err)
	}

	tableClient := storageClient.GetTableService()
	return &tableClient, true, nil
}

func (armClient *ArmClient) getQueueServiceClientForStorageAccount(resourceGroupName, storageAccountName string) (*mainStorage.QueueServiceClient, bool, error) {
	key, accountExists, err := armClient.getKeyForStorageAccount(resourceGroupName, storageAccountName)
	if err != nil {
		return nil, accountExists, err
	}
	if accountExists == false {
		return nil, false, nil
	}

	storageClient, err := mainStorage.NewClient(storageAccountName, key, armClient.environment.StorageEndpointSuffix,
		mainStorage.DefaultAPIVersion, true)
	if err != nil {
		return nil, true, fmt.Errorf("Error creating storage client for storage account %q: %s", storageAccountName, err)
	}

	queueClient := storageClient.GetQueueService()
	return &queueClient, true, nil
}
