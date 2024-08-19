package resourceapply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	coreclientv1 "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
)

// Inspired by https://github.com/openshift/library-go/tree/master/pkg/operator/resource/resourceapply

const (
	specHashAnnotation   = "operator.openshift.io/spec-hash"
	generationAnnotation = "operator.openshift.io/generation"

	ConfigCheckFailedEvent = "ConfigurationCheckFailed"

	ResourceCreateSuccessEvent = "ResourceCreateSuccess"
	ResourceCreateFailedEvent  = "ResourceCreateFailed"

	ResourceUpdateSuccessEvent = "ResourceUpdateSuccess"
	ResourceUpdateFailedEvent  = "ResourceUpdateFailed"

	ResourceCreateOrUpdateFailedEvent = "ResourceCreateOrUpdateFailed"

	ResourceRecreatingEvent = "ResourceRecreating"
	RecreateSuccessEvent    = "ResourceRecreateSuccess"

	ResourceDeleteFailedEvent = "ResourceDeleteFailed"
)

// setSpecHashAnnotation computes the hash of the provided spec and sets an annotation of the
// hash on the provided ObjectMeta. This method is used internally by Apply<type> methods, and
// is exposed to support testing with fake clients that need to know the mutated form of the
// resource resulting from an Apply<type> call.
func setSpecHashAnnotation(objMeta *metav1.ObjectMeta, spec interface{}) error {
	jsonBytes, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	specHash := fmt.Sprintf("%x", sha256.Sum256(jsonBytes))
	if objMeta.Annotations == nil {
		objMeta.Annotations = map[string]string{}
	}
	objMeta.Annotations[specHashAnnotation] = specHash
	return nil
}

// ApplyResource applies resources of unspecified type
func ApplyResource(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, resource client.Object) (bool, error) {
	switch t := resource.(type) {
	case *appsv1.Deployment:
		return applyDeployment(ctx, client, recorder, t)
	case *appsv1.DaemonSet:
		return applyDaemonSet(ctx, client, recorder, t)
	case *corev1.ConfigMap:
		return applyConfigMap(ctx, client, recorder, t)
	case *policyv1.PodDisruptionBudget:
		return applyPodDisruptionBudget(ctx, client, recorder, t)
	case *rbacv1.Role:
		return applyRole(ctx, client, recorder, t)
	case *rbacv1.ClusterRole:
		return applyClusterRole(ctx, client, recorder, t)
	case *rbacv1.RoleBinding:
		return applyRoleBinding(ctx, client, recorder, t)
	case *rbacv1.ClusterRoleBinding:
		return applyClusterRoleBinding(ctx, client, recorder, t)
	case *admissionregistrationv1.ValidatingAdmissionPolicy:
		return applyValidatingAdmissionPolicy(ctx, client, recorder, t)
	case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
		return applyValidatingAdmissionPolicyBinding(ctx, client, recorder, t)
	default:
		return false, fmt.Errorf("unhandled type %T", resource)
	}
}

func applyConfigMap(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *corev1.ConfigMap) (bool, error) {
	required := requiredOriginal.DeepCopy()
	existing := &corev1.ConfigMap{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(requiredOriginal), existing)
	if apierrors.IsNotFound(err) {
		err := client.Create(ctx, resourcemerge.WithCleanLabelsAndAnnotations(required).(*corev1.ConfigMap))
		if err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)

	var modifiedKeys []string
	for existingCopyKey, existingCopyValue := range existingCopy.Data {
		if requiredValue, ok := required.Data[existingCopyKey]; !ok || (existingCopyValue != requiredValue) {
			modifiedKeys = append(modifiedKeys, "data."+existingCopyKey)
		}
	}
	for existingCopyKey, existingCopyBinValue := range existingCopy.BinaryData {
		if requiredBinValue, ok := required.BinaryData[existingCopyKey]; !ok || !bytes.Equal(existingCopyBinValue, requiredBinValue) {
			modifiedKeys = append(modifiedKeys, "binaryData."+existingCopyKey)
		}
	}
	for requiredKey := range required.Data {
		if _, ok := existingCopy.Data[requiredKey]; !ok {
			modifiedKeys = append(modifiedKeys, "data."+requiredKey)
		}
	}
	for requiredBinKey := range required.BinaryData {
		if _, ok := existingCopy.BinaryData[requiredBinKey]; !ok {
			modifiedKeys = append(modifiedKeys, "binaryData."+requiredBinKey)
		}
	}

	dataSame := len(modifiedKeys) == 0
	if dataSame && !*modified {
		return false, nil
	}
	existingCopy.Data = required.Data
	existingCopy.BinaryData = required.BinaryData

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier

	err = client.Update(ctx, toWrite)
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(toWrite, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, err
}

func applyDeployment(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *appsv1.Deployment) (bool, error) {
	required := requiredOriginal.DeepCopy()
	if err := annotatePodSpecWithRelatedConfigsHash(ctx, client, required.Namespace, &required.Spec.Template); err != nil {
		klog.V(3).Infof("Can not check related configs for %s/%s: %v", required.GetObjectKind(), required.GetName(), err)
		recorder.Event(required, corev1.EventTypeWarning, ConfigCheckFailedEvent, err.Error())
	}
	if err := setSpecHashAnnotation(&required.ObjectMeta, required.Spec); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceCreateOrUpdateFailedEvent, err.Error())
		return false, err
	}

	existing := &appsv1.Deployment{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		required.Annotations[generationAnnotation] = "1"
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()

	expectedGeneration := ""
	if _, ok := existingCopy.Annotations[generationAnnotation]; ok {
		expectedGeneration = existingCopy.Annotations[generationAnnotation]
	}

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	if !*modified && expectedGeneration == fmt.Sprintf("%x", existingCopy.GetGeneration()) {
		return false, nil
	}

	// Check if deployment recreation needed
	// Currently it is necessary if pod selector was changed
	needRecreate := false
	if !reflect.DeepEqual(existingCopy.Spec.Selector, required.Spec.Selector) {
		needRecreate = true
	}
	if needRecreate {
		klog.Infof("Deployment need to be recreated with new parameters")
		recorder.Event(
			existing, corev1.EventTypeNormal,
			ResourceRecreatingEvent, "Delete existing deployment to recreate it with new parameters",
		)
		// Perform dry run creation in order to validate deployment before deleting existing one
		requiredCopy := required.DeepCopy()
		requiredCopy.Name = fmt.Sprintf("%s-dry-run", requiredCopy.Name)
		dryRunOpts := &coreclientv1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
		if err := client.Create(ctx, requiredCopy, dryRunOpts); err != nil {
			recorder.Event(existing, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("new resource validation prior to old resource deletion failed: %v", err)
		}

		if err := client.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			recorder.Event(existing, corev1.EventTypeWarning, ResourceDeleteFailedEvent, err.Error())
			return false, fmt.Errorf("old resource deletion failed: %v", err)
		}

		required.Annotations[generationAnnotation] = "1"
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("deployment recreation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, RecreateSuccessEvent, "Resource was successfully recreated")
		return true, nil
	}

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = *required.Spec.DeepCopy()

	toWrite.Annotations[generationAnnotation] = fmt.Sprintf("%x", existingCopy.GetGeneration()+1)

	if err := client.Update(ctx, toWrite); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyDaemonSet(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *appsv1.DaemonSet) (bool, error) {
	required := requiredOriginal.DeepCopy()
	if err := annotatePodSpecWithRelatedConfigsHash(ctx, client, required.Namespace, &required.Spec.Template); err != nil {
		klog.V(3).Infof("Can not check related configs for %s/%s: %v", required.GetObjectKind(), required.GetName(), err)
		recorder.Event(required, corev1.EventTypeWarning, ConfigCheckFailedEvent, err.Error())
	}
	if err := setSpecHashAnnotation(&required.ObjectMeta, required.Spec); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceCreateOrUpdateFailedEvent, err.Error())
		return false, err
	}

	existing := &appsv1.DaemonSet{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		required.Annotations[generationAnnotation] = "1"
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()

	expectedGeneration := ""
	if _, ok := existingCopy.Annotations[generationAnnotation]; ok {
		expectedGeneration = existingCopy.Annotations[generationAnnotation]
	}

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	if !*modified && expectedGeneration == fmt.Sprintf("%x", existingCopy.GetGeneration()) {
		return false, nil
	}

	// Check if ds recreation needed
	// Currently it is necessary if pod selector was changed
	needRecreate := false
	if !reflect.DeepEqual(existingCopy.Spec.Selector, required.Spec.Selector) {
		needRecreate = true
	}
	if needRecreate {
		klog.Infof("DaemonSet need to be recreated with new parameters")
		recorder.Event(
			existing, corev1.EventTypeNormal,
			ResourceRecreatingEvent, "Delete existing daemonset to recreate it with new parameters",
		)
		// Perform dry run creation in order to validate ds before deleting existing one
		requiredCopy := required.DeepCopy()
		requiredCopy.Name = fmt.Sprintf("%s-dry-run", requiredCopy.Name)
		dryRunOpts := &coreclientv1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
		if err := client.Create(ctx, requiredCopy, dryRunOpts); err != nil {
			recorder.Event(existing, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("new resource validation prior to old resource deletion failed: %v", err)
		}

		if err := client.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			recorder.Event(existing, corev1.EventTypeWarning, ResourceDeleteFailedEvent, err.Error())
			return false, fmt.Errorf("old resource deletion failed: %v", err)
		}

		required.Annotations[generationAnnotation] = "1"
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("ds recreation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, RecreateSuccessEvent, "Resource was successfully recreated")
		return true, nil
	}

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = *required.Spec.DeepCopy()

	toWrite.Annotations[generationAnnotation] = fmt.Sprintf("%x", existingCopy.GetGeneration()+1)

	if err := client.Update(ctx, toWrite); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyPodDisruptionBudget(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *policyv1.PodDisruptionBudget) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &policyv1.PodDisruptionBudget{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("pdb creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get pdb for update: %v", err)
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	contentSame := equality.Semantic.DeepEqual(existingCopy.Spec, required.Spec)

	if !*modified && contentSame {
		return false, nil
	}

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = *required.Spec.DeepCopy()

	if err := client.Update(ctx, toWrite); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyRole(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *rbacv1.Role) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &rbacv1.Role{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("role creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get role for update: %v", err)
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	contentSame := equality.Semantic.DeepEqual(existingCopy.Rules, required.Rules)

	if !*modified && contentSame {
		return false, nil
	}

	existingCopy.Rules = required.Rules

	if err := client.Update(ctx, existingCopy); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyClusterRole(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *rbacv1.ClusterRole) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &rbacv1.ClusterRole{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("clusterrole creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get clusterrole for update: %v", err)
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	contentSame := equality.Semantic.DeepEqual(existingCopy.Rules, required.Rules)

	if !*modified && contentSame {
		return false, nil
	}

	existingCopy.Rules = required.Rules
	existingCopy.AggregationRule = nil

	if err := client.Update(ctx, existingCopy); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyRoleBinding(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *rbacv1.RoleBinding) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &rbacv1.RoleBinding{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("rolebinding creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get rolebinding for update: %v", err)
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()
	requiredCopy := required.DeepCopy()

	// Enforce apiGroup fields in roleRefs
	existingCopy.RoleRef.APIGroup = rbacv1.GroupName
	for i := range existingCopy.Subjects {
		if existingCopy.Subjects[i].Kind == "User" {
			existingCopy.Subjects[i].APIGroup = rbacv1.GroupName
		}
	}

	requiredCopy.RoleRef.APIGroup = rbacv1.GroupName
	for i := range requiredCopy.Subjects {
		if requiredCopy.Subjects[i].Kind == "User" {
			requiredCopy.Subjects[i].APIGroup = rbacv1.GroupName
		}
	}

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, requiredCopy.ObjectMeta)

	subjectsAreSame := equality.Semantic.DeepEqual(existingCopy.Subjects, requiredCopy.Subjects)
	roleRefIsSame := equality.Semantic.DeepEqual(existingCopy.RoleRef, requiredCopy.RoleRef)

	if subjectsAreSame && roleRefIsSame && !*modified {
		return false, nil
	}

	existingCopy.Subjects = requiredCopy.Subjects
	existingCopy.RoleRef = requiredCopy.RoleRef

	if err := client.Update(ctx, existingCopy); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyClusterRoleBinding(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *rbacv1.ClusterRoleBinding) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &rbacv1.ClusterRoleBinding{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("clusterrolebinding creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get clusterrolebinding for update: %v", err)
	}

	modified := ptr.To[bool](false)
	existingCopy := existing.DeepCopy()
	requiredCopy := required.DeepCopy()

	// Enforce apiGroup fields in roleRefs
	existingCopy.RoleRef.APIGroup = rbacv1.GroupName
	for i := range existingCopy.Subjects {
		if existingCopy.Subjects[i].Kind == "User" {
			existingCopy.Subjects[i].APIGroup = rbacv1.GroupName
		}
	}

	requiredCopy.RoleRef.APIGroup = rbacv1.GroupName
	for i := range requiredCopy.Subjects {
		if requiredCopy.Subjects[i].Kind == "User" {
			requiredCopy.Subjects[i].APIGroup = rbacv1.GroupName
		}
	}

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, requiredCopy.ObjectMeta)

	subjectsAreSame := equality.Semantic.DeepEqual(existingCopy.Subjects, requiredCopy.Subjects)
	roleRefIsSame := equality.Semantic.DeepEqual(existingCopy.RoleRef, requiredCopy.RoleRef)

	if subjectsAreSame && roleRefIsSame && !*modified {
		return false, nil
	}

	existingCopy.Subjects = requiredCopy.Subjects
	existingCopy.RoleRef = requiredCopy.RoleRef

	if err := client.Update(ctx, existingCopy); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")
	return true, nil
}

func applyValidatingAdmissionPolicy(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder,
	requiredOriginal *admissionregistrationv1.ValidatingAdmissionPolicy) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(requiredOriginal), existing)
	if apierrors.IsNotFound(err) {
		required := requiredOriginal.DeepCopy()
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("validatingadmissionpolicy creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	} else if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get validatingadmissionpolicy for update: %v", err)
	}

	modified := false
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(&modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	specEquivalent := equality.Semantic.DeepDerivative(required.Spec, existingCopy.Spec)
	if specEquivalent && !modified {
		return false, nil
	}
	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = required.Spec

	klog.V(2).Infof("ValidatingAdmissionPolicyConfiguration %q changes: %v", required.GetNamespace()+"/"+required.GetName(), resourceapply.JSONPatchNoError(existing, toWrite))

	if err := client.Update(ctx, existingCopy); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")

	return true, nil
}

func applyValidatingAdmissionPolicyBinding(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder,
	requiredOriginal *admissionregistrationv1.ValidatingAdmissionPolicyBinding) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(requiredOriginal), existing)
	if apierrors.IsNotFound(err) {
		required := requiredOriginal.DeepCopy()
		if err := client.Create(ctx, required); err != nil {
			recorder.Event(required, corev1.EventTypeWarning, ResourceCreateFailedEvent, err.Error())
			return false, fmt.Errorf("validatingadmissionpolicybinding creation failed: %v", err)
		}
		recorder.Event(required, corev1.EventTypeNormal, ResourceCreateSuccessEvent, "Resource was successfully created")
		return true, nil
	} else if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, fmt.Errorf("failed to get validatingadmissionpolicybinding for update: %v", err)
	}

	modified := false
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(&modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	specEquivalent := equality.Semantic.DeepDerivative(required.Spec, existingCopy.Spec)
	if specEquivalent && !modified {
		return false, nil
	}
	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = required.Spec

	klog.V(2).Infof("ValidatingAdmissionPolicyBindingConfiguration %q changes: %v", required.GetNamespace()+"/"+required.GetName(), resourceapply.JSONPatchNoError(existing, toWrite))

	if err := client.Update(ctx, existingCopy); err != nil {
		recorder.Event(required, corev1.EventTypeWarning, ResourceUpdateFailedEvent, err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, ResourceUpdateSuccessEvent, "Resource was successfully updated")

	return true, nil
}
