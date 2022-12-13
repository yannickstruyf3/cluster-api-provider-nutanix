/*
Copyright 2022 Nutanix

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	infrav1 "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
	nutanixClientHelper "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/pkg/client"
	nutanixClient "github.com/nutanix-cloud-native/prism-go-client"
	"github.com/nutanix-cloud-native/prism-go-client/utils"
	nutanixClientV3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	"k8s.io/apimachinery/pkg/api/resource"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/klog/v2"
)

const (
	providerIdPrefix = "nutanix://"

	taskSucceededMessage = "SUCCEEDED"
	serviceNamePECluster = "AOS"
)

type Helpers interface {
	CreateNutanixClient(secretInformer coreinformers.SecretInformer, cmInformer coreinformers.ConfigMapInformer, nutanixCluster *infrav1.NutanixCluster) (*nutanixClientV3.Client, error)
	CreateSystemDiskSpec(imageUUID string, systemDiskSize int64) (*nutanixClientV3.VMDisk, error)
	DeleteCategories(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifiers []*infrav1.NutanixCategoryIdentifier) error
	DeleteVM(ctx context.Context, client *nutanixClientV3.Client, vmName, vmUUID string) (string, error)
	FindVM(ctx context.Context, client *nutanixClientV3.Client, nutanixMachine *infrav1.NutanixMachine) (*nutanixClientV3.VMIntentResponse, error)
	FindVMByName(ctx context.Context, client *nutanixClientV3.Client, vmName string) (*nutanixClientV3.VMIntentResponse, error)
	FindVMByUUID(ctx context.Context, client *nutanixClientV3.Client, uuid string) (*nutanixClientV3.VMIntentResponse, error)
	GenerateProviderID(uuid string) string
	GetCategoryVMSpec(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifiers []*infrav1.NutanixCategoryIdentifier) (map[string]string, error)
	GetDefaultCAPICategoryIdentifiers(clusterName string) []*infrav1.NutanixCategoryIdentifier
	GetImageUUID(ctx context.Context, client *nutanixClientV3.Client, imageName, imageUUID *string) (string, error)
	GetMibValueOfQuantity(quantity resource.Quantity) int64
	GetOrCreateCategories(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifiers []*infrav1.NutanixCategoryIdentifier) ([]*nutanixClientV3.CategoryValueStatus, error)
	GetPEUUID(ctx context.Context, client *nutanixClientV3.Client, peName, peUUID *string) (string, error)
	GetProjectUUID(ctx context.Context, client *nutanixClientV3.Client, projectName, projectUUID *string) (string, error)
	GetSubnetUUID(ctx context.Context, client *nutanixClientV3.Client, peUUID string, subnetName, subnetUUID *string) (string, error)
	GetSubnetUUIDList(ctx context.Context, client *nutanixClientV3.Client, machineSubnets []infrav1.NutanixResourceIdentifier, peUUID string) ([]string, error)
	GetTaskUUIDFromVM(vm *nutanixClientV3.VMIntentResponse) (string, error)
	GetVMUUID(ctx context.Context, nutanixMachine *infrav1.NutanixMachine) (string, error)
	HasTaskInProgress(ctx context.Context, client *nutanixClientV3.Client, taskUUID string) (bool, error)
}

type CapxHelpers struct{}

func (c *CapxHelpers) CreateNutanixClient(secretInformer coreinformers.SecretInformer, cmInformer coreinformers.ConfigMapInformer, nutanixCluster *infrav1.NutanixCluster) (*nutanixClientV3.Client, error) {
	helper, err := nutanixClientHelper.NewNutanixClientHelper(secretInformer, cmInformer)
	if err != nil {
		klog.Errorf("error creating nutanix client helper: %v", err)
		return nil, err
	}
	return helper.GetClientFromEnvironment(nutanixCluster)
}

// deleteVM deletes a VM and is invoked by the NutanixMachineReconciler
func (c *CapxHelpers) DeleteVM(ctx context.Context, client *nutanixClientV3.Client, vmName, vmUUID string) (string, error) {
	var err error

	if vmUUID == "" {
		klog.Warning("VmUUID was empty. Skipping delete")
		return "", nil
	}

	klog.Infof("Deleting VM %s with UUID: %s", vmName, vmUUID)
	vmDeleteResponse, err := client.V3.DeleteVM(ctx, vmUUID)
	if err != nil {
		klog.Infof("Error deleting machine %s", vmName)
		return "", err
	}
	deleteTaskUUID := vmDeleteResponse.Status.ExecutionContext.TaskUUID.(string)

	return deleteTaskUUID, nil
}

// FindVMByUUID retrieves the VM with the given vm UUID. Returns nil if not found
func (c *CapxHelpers) FindVMByUUID(ctx context.Context, client *nutanixClientV3.Client, uuid string) (*nutanixClientV3.VMIntentResponse, error) {
	klog.Infof("Checking if VM with UUID %s exists.", uuid)

	response, err := client.V3.GetVM(ctx, uuid)
	if err != nil {
		if strings.Contains(fmt.Sprint(err), "ENTITY_NOT_FOUND") {
			klog.Infof("vm with uuid %s does not exist.", uuid)
			return nil, nil
		} else {
			klog.Errorf("Failed to find VM by vmUUID %s. error: %v", uuid, err)
			return nil, err
		}
	}

	return response, nil
}

func (c *CapxHelpers) GenerateProviderID(uuid string) string {
	return fmt.Sprintf("%s%s", providerIdPrefix, uuid)
}

func (c *CapxHelpers) GetVMUUID(ctx context.Context, nutanixMachine *infrav1.NutanixMachine) (string, error) {
	vmUUID := nutanixMachine.Status.VmUUID
	if vmUUID != "" {
		if _, err := uuid.Parse(vmUUID); err != nil {
			return "", fmt.Errorf("VMUUID was set but was not a valid UUID: %s err: %v", vmUUID, err)
		}
		return vmUUID, nil
	}
	providerID := nutanixMachine.Spec.ProviderID
	if providerID == "" {
		return "", nil
	}
	id := strings.TrimPrefix(providerID, providerIdPrefix)
	// Not returning error since the ProviderID initially is not a UUID. CAPX only sets the UUID after VM provisioning. If it is not a UUID, continue.
	if _, err := uuid.Parse(id); err != nil {
		return "", nil
	}
	return id, nil
}

func (c *CapxHelpers) FindVM(ctx context.Context, client *nutanixClientV3.Client, nutanixMachine *infrav1.NutanixMachine) (*nutanixClientV3.VMIntentResponse, error) {
	vmName := nutanixMachine.Name
	vmUUID, err := c.GetVMUUID(ctx, nutanixMachine)
	if err != nil {
		return nil, err
	}
	// Search via uuid if it is present
	if vmUUID != "" {
		klog.Infof("Searching for VM %s using UUID %s", vmName, vmUUID)
		vm, err := c.FindVMByUUID(ctx, client, vmUUID)
		if err != nil {
			klog.Errorf("error occurred finding VM with uuid %s: %v", vmUUID, err)
			return nil, err
		}
		if vm == nil {
			errorMsg := fmt.Sprintf("no vm %s found with UUID %s but was expected to be present", vmName, vmUUID)
			klog.Error(errorMsg)
			return nil, fmt.Errorf(errorMsg)
		}
		if *vm.Spec.Name != vmName {
			errorMsg := fmt.Sprintf("found VM with UUID %s but name did not match %s. error: %v", vmUUID, vmName, err)
			klog.Errorf(errorMsg)
			return nil, fmt.Errorf(errorMsg)
		}
		return vm, nil
		// otherwise search via name
	} else {
		klog.Infof("Searching for VM %s using name", vmName)
		vm, err := c.FindVMByName(ctx, client, vmName)
		if err != nil {
			klog.Errorf("error occurred finding VM %s by name: %v", vmName, err)
			return nil, err
		}
		return vm, nil
	}
}

// FindVMByName retrieves the VM with the given vm name
func (c *CapxHelpers) FindVMByName(ctx context.Context, client *nutanixClientV3.Client, vmName string) (*nutanixClientV3.VMIntentResponse, error) {
	klog.Infof("Checking if VM with name %s exists.", vmName)

	res, err := client.V3.ListVM(ctx, &nutanixClientV3.DSMetadata{
		Filter: utils.StringPtr(fmt.Sprintf("vm_name==%s", vmName)),
	})
	if err != nil {
		errorMsg := fmt.Errorf("error occurred when searching for VM by name %s. error: %v", vmName, err)
		klog.Error(errorMsg)
		return nil, errorMsg
	}

	if len(res.Entities) > 1 {
		errorMsg := fmt.Sprintf("Found more than one (%v) vms with name %s.", len(res.Entities), vmName)
		klog.Errorf(errorMsg)
		return nil, fmt.Errorf(errorMsg)
	}

	if len(res.Entities) == 0 {
		return nil, nil
	}

	return c.FindVMByUUID(ctx, client, *res.Entities[0].Metadata.UUID)
}

func (c *CapxHelpers) GetPEUUID(ctx context.Context, client *nutanixClientV3.Client, peName, peUUID *string) (string, error) {
	if peUUID == nil && peName == nil {
		return "", fmt.Errorf("cluster name or uuid must be passed in order to retrieve the pe")
	}
	if peUUID != nil && *peUUID != "" {
		peIntentResponse, err := client.V3.GetCluster(ctx, *peUUID)
		if err != nil {
			if strings.Contains(fmt.Sprint(err), "ENTITY_NOT_FOUND") {
				return "", fmt.Errorf("failed to find Prism Element cluster with UUID %s: %v", *peUUID, err)
			}
		}
		return *peIntentResponse.Metadata.UUID, nil
	} else if peName != nil && *peName != "" {
		filter := c.getFilterForName(*peName)
		responsePEs, err := client.V3.ListAllCluster(ctx, filter)
		if err != nil {
			return "", err
		}
		// Validate filtered PEs
		foundPEs := make([]*nutanixClientV3.ClusterIntentResponse, 0)
		for _, s := range responsePEs.Entities {
			peSpec := s.Spec
			if *peSpec.Name == *peName && c.hasPEClusterServiceEnabled(s, serviceNamePECluster) {
				foundPEs = append(foundPEs, s)
			}
		}
		if len(foundPEs) == 1 {
			return *foundPEs[0].Metadata.UUID, nil
		}
		if len(foundPEs) == 0 {
			return "", fmt.Errorf("failed to retrieve Prism Element cluster by name %s", *peName)
		} else {
			return "", fmt.Errorf("more than one Prism Element cluster found with name %s", *peName)
		}
	}
	return "", fmt.Errorf("failed to retrieve Prism Element cluster by name or uuid. Verify input parameters.")
}

// GetMibValueOfQuantity returns the given quantity value in Mib
func (c *CapxHelpers) GetMibValueOfQuantity(quantity resource.Quantity) int64 {
	return quantity.Value() / (1024 * 1024)
}

func (c *CapxHelpers) CreateSystemDiskSpec(imageUUID string, systemDiskSize int64) (*nutanixClientV3.VMDisk, error) {
	if imageUUID == "" {
		return nil, fmt.Errorf("image UUID must be set when creating system disk")
	}
	if systemDiskSize <= 0 {
		return nil, fmt.Errorf("invalid system disk size: %d. Provide in XXGi (for example 70Gi) format instead", systemDiskSize)
	}
	systemDisk := &nutanixClientV3.VMDisk{
		DataSourceReference: &nutanixClientV3.Reference{
			Kind: utils.StringPtr("image"),
			UUID: utils.StringPtr(imageUUID),
		},
		DiskSizeMib: utils.Int64Ptr(systemDiskSize),
	}
	return systemDisk, nil
}

func (c *CapxHelpers) GetSubnetUUID(ctx context.Context, client *nutanixClientV3.Client, peUUID string, subnetName, subnetUUID *string) (string, error) {
	var foundSubnetUUID string
	if subnetUUID == nil && subnetName == nil {
		return "", fmt.Errorf("subnet name or subnet uuid must be passed in order to retrieve the subnet")
	}
	if subnetUUID != nil {
		subnetIntentResponse, err := client.V3.GetSubnet(ctx, *subnetUUID)
		if err != nil {
			if strings.Contains(fmt.Sprint(err), "ENTITY_NOT_FOUND") {
				return "", fmt.Errorf("failed to find subnet with UUID %s: %v", *subnetUUID, err)
			}
		}
		foundSubnetUUID = *subnetIntentResponse.Metadata.UUID
	} else if subnetName != nil {
		filter := c.getFilterForName(*subnetName)
		subnetClientSideFilter := c.getSubnetClientSideFilter(peUUID)
		responseSubnets, err := client.V3.ListAllSubnet(ctx, filter, subnetClientSideFilter)
		if err != nil {
			return "", err
		}
		// Validate filtered Subnets
		foundSubnets := make([]*nutanixClientV3.SubnetIntentResponse, 0)
		for _, s := range responseSubnets.Entities {
			subnetSpec := s.Spec
			if *subnetSpec.Name == *subnetName && *subnetSpec.ClusterReference.UUID == peUUID {
				foundSubnets = append(foundSubnets, s)
			}
		}
		if len(foundSubnets) == 0 {
			return "", fmt.Errorf("failed to retrieve subnet by name %s", *subnetName)
		} else if len(foundSubnets) > 1 {
			return "", fmt.Errorf("more than one subnet found with name %s", *subnetName)
		} else {
			foundSubnetUUID = *foundSubnets[0].Metadata.UUID
		}
		if foundSubnetUUID == "" {
			return "", fmt.Errorf("failed to retrieve subnet by name or uuid. Verify input parameters.")
		}
	}
	return foundSubnetUUID, nil
}

func (c *CapxHelpers) GetImageUUID(ctx context.Context, client *nutanixClientV3.Client, imageName, imageUUID *string) (string, error) {
	var foundImageUUID string

	if imageUUID == nil && imageName == nil {
		return "", fmt.Errorf("image name or image uuid must be passed in order to retrieve the image")
	}
	if imageUUID != nil {
		imageIntentResponse, err := client.V3.GetImage(ctx, *imageUUID)
		if err != nil {
			if strings.Contains(fmt.Sprint(err), "ENTITY_NOT_FOUND") {
				return "", fmt.Errorf("failed to find image with UUID %s: %v", *imageUUID, err)
			}
		}
		foundImageUUID = *imageIntentResponse.Metadata.UUID
	} else if imageName != nil {
		filter := c.getFilterForName(*imageName)
		responseImages, err := client.V3.ListAllImage(ctx, filter)
		if err != nil {
			return "", err
		}
		// Validate filtered Images
		foundImages := make([]*nutanixClientV3.ImageIntentResponse, 0)
		for _, s := range responseImages.Entities {
			imageSpec := s.Spec
			if *imageSpec.Name == *imageName {
				foundImages = append(foundImages, s)
			}
		}
		if len(foundImages) == 0 {
			return "", fmt.Errorf("failed to retrieve image by name %s", *imageName)
		} else if len(foundImages) > 1 {
			return "", fmt.Errorf("more than one image found with name %s", *imageName)
		} else {
			foundImageUUID = *foundImages[0].Metadata.UUID
		}
		if foundImageUUID == "" {
			return "", fmt.Errorf("failed to retrieve image by name or uuid. Verify input parameters.")
		}
	}
	return foundImageUUID, nil
}

func (c *CapxHelpers) HasTaskInProgress(ctx context.Context, client *nutanixClientV3.Client, taskUUID string) (bool, error) {
	taskStatus, err := nutanixClientHelper.GetTaskState(ctx, client, taskUUID)
	if err != nil {
		return false, err
	}
	if taskStatus != taskSucceededMessage {
		klog.Infof("VM task with UUID %s still in progress: %s. Requeuing", taskUUID, taskStatus)
		return true, nil
	}
	return false, nil
}

func (c *CapxHelpers) GetTaskUUIDFromVM(vm *nutanixClientV3.VMIntentResponse) (string, error) {
	if vm == nil {
		return "", fmt.Errorf("cannot extract task uuid from empty vm object")
	}
	taskInterface := vm.Status.ExecutionContext.TaskUUID
	vmName := *vm.Spec.Name

	switch t := reflect.TypeOf(taskInterface).Kind(); t {
	case reflect.Slice:
		l := taskInterface.([]interface{})
		if len(l) != 1 {
			return "", fmt.Errorf("did not find expected amount of task UUIDs for VM %s", vmName)
		}
		return l[0].(string), nil
	case reflect.String:
		return taskInterface.(string), nil
	default:
		return "", fmt.Errorf("invalid type found for task uuid extracted from vm %s: %v", vmName, t)
	}
}

func (c *CapxHelpers) GetSubnetUUIDList(ctx context.Context, client *nutanixClientV3.Client, machineSubnets []infrav1.NutanixResourceIdentifier, peUUID string) ([]string, error) {
	subnetUUIDs := make([]string, 0)
	for _, machineSubnet := range machineSubnets {
		subnetUUID, err := c.GetSubnetUUID(
			ctx,
			client,
			peUUID,
			machineSubnet.Name,
			machineSubnet.UUID,
		)
		if err != nil {
			return subnetUUIDs, err
		}
		subnetUUIDs = append(subnetUUIDs, subnetUUID)
	}
	return subnetUUIDs, nil
}

func (c *CapxHelpers) GetDefaultCAPICategoryIdentifiers(clusterName string) []*infrav1.NutanixCategoryIdentifier {
	return []*infrav1.NutanixCategoryIdentifier{
		{
			Key:   fmt.Sprintf("%s%s", infrav1.DefaultCAPICategoryPrefix, clusterName),
			Value: infrav1.DefaultCAPICategoryOwnedValue,
		},
	}
}

func (c *CapxHelpers) GetOrCreateCategories(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifiers []*infrav1.NutanixCategoryIdentifier) ([]*nutanixClientV3.CategoryValueStatus, error) {
	categories := make([]*nutanixClientV3.CategoryValueStatus, 0)
	for _, ci := range categoryIdentifiers {
		if ci == nil {
			return categories, fmt.Errorf("cannot get or create nil category")
		}
		category, err := c.getOrCreateCategory(ctx, client, ci)
		if err != nil {
			return categories, err
		}
		categories = append(categories, category)
	}
	return categories, nil
}

func (c *CapxHelpers) getCategoryKey(ctx context.Context, client *nutanixClientV3.Client, key string) (*nutanixClientV3.CategoryKeyStatus, error) {
	categoryKey, err := client.V3.GetCategoryKey(ctx, key)
	if err != nil {
		if !strings.Contains(fmt.Sprint(err), "ENTITY_NOT_FOUND") {
			errorMsg := fmt.Errorf("Failed to retrieve category with key %s. error: %v", key, err)
			klog.Error(errorMsg)
			return nil, errorMsg
		} else {
			return nil, nil
		}
	}
	return categoryKey, nil
}

func (c *CapxHelpers) getCategoryValue(ctx context.Context, client *nutanixClientV3.Client, key, value string) (*nutanixClientV3.CategoryValueStatus, error) {
	categoryValue, err := client.V3.GetCategoryValue(ctx, key, value)
	if err != nil {
		if !strings.Contains(fmt.Sprint(err), "CATEGORY_NAME_VALUE_MISMATCH") {
			errorMsg := fmt.Errorf("failed to retrieve category value %s in category %s. error: %v", value, key, err)
			klog.Error(errorMsg)
			return nil, errorMsg
		} else {
			return nil, nil
		}
	}
	return categoryValue, nil
}

func (c *CapxHelpers) DeleteCategories(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifiers []*infrav1.NutanixCategoryIdentifier) error {
	groupCategoriesByKey := make(map[string][]string, 0)
	for _, ci := range categoryIdentifiers {
		ciKey := ci.Key
		ciValue := ci.Value
		if gck, ok := groupCategoriesByKey[ciKey]; ok {
			groupCategoriesByKey[ciKey] = append(gck, ciValue)
		} else {
			groupCategoriesByKey[ciKey] = []string{ciValue}
		}
	}

	for key, values := range groupCategoriesByKey {
		klog.Infof("Retrieving category with key %s", key)
		categoryKey, err := c.getCategoryKey(ctx, client, key)
		if err != nil {
			errorMsg := fmt.Errorf("failed to retrieve category with key %s. error: %v", key, err)
			klog.Error(errorMsg)
			return errorMsg
		}
		klog.Infof("Category with key %s found. Starting deletion of values", key)
		if categoryKey == nil {
			klog.Infof("Category with key %s not found. Already deleted?", key)
			continue
		}
		for _, value := range values {
			categoryValue, err := c.getCategoryValue(ctx, client, key, value)
			if err != nil {
				errorMsg := fmt.Errorf("failed to retrieve category value %s in category %s. error: %v", value, key, err)
				klog.Error(errorMsg)
				return errorMsg
			}
			if categoryValue == nil {
				klog.Infof("Category with value %s in category %s not found. Already deleted?", value, key)
				continue
			}

			err = client.V3.DeleteCategoryValue(ctx, key, value)
			if err != nil {
				errorMsg := fmt.Errorf("failed to delete category with key %s. error: %v", key, err)
				return errorMsg
			}
		}
		// check if there are remaining category values
		categoryKeyValues, err := client.V3.ListCategoryValues(ctx, key, &nutanixClientV3.CategoryListMetadata{})
		if err != nil {
			errorMsg := fmt.Errorf("failed to get values of category with key %s: %v", key, err)
			klog.Error(errorMsg)
			return errorMsg
		}
		if len(categoryKeyValues.Entities) > 0 {
			errorMsg := fmt.Errorf("cannot remove category with key %s because it still has category values assigned", key)
			klog.Error(errorMsg)
			return errorMsg
		}
		klog.Infof("No values assigned to category. Removing category with key %s", key)
		err = client.V3.DeleteCategoryKey(ctx, key)
		if err != nil {
			errorMsg := fmt.Errorf("failed to delete category with key %s: %v", key, err)
			klog.Error(errorMsg)
			return errorMsg
		}
	}
	return nil
}

func (c *CapxHelpers) getOrCreateCategory(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifier *infrav1.NutanixCategoryIdentifier) (*nutanixClientV3.CategoryValueStatus, error) {
	if categoryIdentifier == nil {
		return nil, fmt.Errorf("category identifier cannot be nil when getting or creating categories")
	}
	if categoryIdentifier.Key == "" {
		return nil, fmt.Errorf("category identifier key must be set when when getting or creating categories")
	}
	if categoryIdentifier.Value == "" {
		return nil, fmt.Errorf("category identifier key must be set when when getting or creating categories")
	}
	klog.Infof("Checking existence of category with key %s", categoryIdentifier.Key)
	categoryKey, err := c.getCategoryKey(ctx, client, categoryIdentifier.Key)
	if err != nil {
		errorMsg := fmt.Errorf("Failed to retrieve category with key %s. error: %v", categoryIdentifier.Key, err)
		klog.Error(errorMsg)
		return nil, errorMsg
	}
	if categoryKey == nil {
		klog.Infof("Category with key %s did not exist.", categoryIdentifier.Key)
		categoryKey, err = client.V3.CreateOrUpdateCategoryKey(ctx, &nutanixClientV3.CategoryKey{
			Description: utils.StringPtr(infrav1.DefaultCAPICategoryDescription),
			Name:        utils.StringPtr(categoryIdentifier.Key),
		})
		if err != nil {
			errorMsg := fmt.Errorf("Failed to create category with key %s. error: %v", categoryIdentifier.Key, err)
			klog.Error(errorMsg)
			return nil, errorMsg
		}
	}
	categoryValue, err := c.getCategoryValue(ctx, client, *categoryKey.Name, categoryIdentifier.Value)
	if err != nil {
		errorMsg := fmt.Errorf("Failed to retrieve category value %s in category %s. error: %v", categoryIdentifier.Value, categoryIdentifier.Key, err)
		klog.Error(errorMsg)
		return nil, errorMsg
	}
	if categoryValue == nil {
		categoryValue, err = client.V3.CreateOrUpdateCategoryValue(ctx, *categoryKey.Name, &nutanixClientV3.CategoryValue{
			Description: utils.StringPtr(infrav1.DefaultCAPICategoryDescription),
			Value:       utils.StringPtr(categoryIdentifier.Value),
		})
		if err != nil {
			klog.Errorf("Failed to create category value %s in category key %s. error: %v", categoryIdentifier.Value, categoryIdentifier.Key, err)
		}
	}
	return categoryValue, nil
}

func (c *CapxHelpers) GetCategoryVMSpec(ctx context.Context, client *nutanixClientV3.Client, categoryIdentifiers []*infrav1.NutanixCategoryIdentifier) (map[string]string, error) {
	categorySpec := map[string]string{}
	for _, ci := range categoryIdentifiers {
		categoryValue, err := c.getCategoryValue(ctx, client, ci.Key, ci.Value)
		if err != nil {
			errorMsg := fmt.Errorf("error occurred while to retrieving category value %s in category %s. error: %v", ci.Value, ci.Key, err)
			klog.Error(errorMsg)
			return nil, errorMsg
		}
		if categoryValue == nil {
			errorMsg := fmt.Errorf("category value %s not found in category %s. error", ci.Value, ci.Key)
			klog.Error(errorMsg)
			return nil, errorMsg
		}
		categorySpec[ci.Key] = ci.Value
	}
	return categorySpec, nil
}

func (c *CapxHelpers) GetProjectUUID(ctx context.Context, client *nutanixClientV3.Client, projectName, projectUUID *string) (string, error) {
	var foundProjectUUID string
	if projectUUID == nil && projectName == nil {
		return "", fmt.Errorf("name or uuid must be passed in order to retrieve the project")
	}
	if projectUUID != nil {
		projectIntentResponse, err := client.V3.GetProject(ctx, *projectUUID)
		if err != nil {
			if strings.Contains(fmt.Sprint(err), "ENTITY_NOT_FOUND") {
				return "", fmt.Errorf("failed to find project with UUID %s: %v", *projectUUID, err)
			}
		}
		foundProjectUUID = *projectIntentResponse.Metadata.UUID
	} else if projectName != nil {
		filter := c.getFilterForName(*projectName)
		responseProjects, err := client.V3.ListAllProject(ctx, filter)
		if err != nil {
			return "", err
		}
		foundProjects := make([]*nutanixClientV3.Project, 0)
		for _, s := range responseProjects.Entities {
			projectSpec := s.Spec
			if projectSpec.Name == *projectName {
				foundProjects = append(foundProjects, s)
			}
		}
		if len(foundProjects) == 0 {
			return "", fmt.Errorf("failed to retrieve project by name %s", *projectName)
		} else if len(foundProjects) > 1 {
			return "", fmt.Errorf("more than one project found with name %s", *projectName)
		} else {
			foundProjectUUID = *foundProjects[0].Metadata.UUID
		}
		if foundProjectUUID == "" {
			return "", fmt.Errorf("failed to retrieve project by name or uuid. Verify input parameters.")
		}
	}
	return foundProjectUUID, nil
}

func (c *CapxHelpers) getSubnetClientSideFilter(peUUID string) []*nutanixClient.AdditionalFilter {
	clientSideFilters := make([]*nutanixClient.AdditionalFilter, 0)
	return append(clientSideFilters, &nutanixClient.AdditionalFilter{
		Name: "cluster_reference.uuid",
		Values: []string{
			peUUID,
		},
	})
}

func (c *CapxHelpers) getFilterForName(name string) string {
	return fmt.Sprintf("name==%s", name)
}

func (c *CapxHelpers) hasPEClusterServiceEnabled(peCluster *nutanixClientV3.ClusterIntentResponse, serviceName string) bool {
	if peCluster.Status == nil ||
		peCluster.Status.Resources == nil ||
		peCluster.Status.Resources.Config == nil {
		return false
	}
	serviceList := peCluster.Status.Resources.Config.ServiceList
	for _, s := range serviceList {
		if s != nil && strings.ToUpper(*s) == serviceName {
			return true
		}
	}
	return false
}
