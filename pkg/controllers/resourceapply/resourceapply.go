package resourceapply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/api/equality"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/client"
	coreclientv1 "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
)

// Inspired by https://github.com/openshift/library-go/tree/master/pkg/operator/resource/resourceapply

const (
	specHashAnnotation   = "operator.openshift.io/spec-hash"
	generationAnnotation = "operator.openshift.io/generation"
)

// SetSpecHashAnnotation computes the hash of the provided spec and sets an annotation of the
// hash on the provided ObjectMeta. This method is used internally by Apply<type> methods, and
// is exposed to support testing with fake clients that need to know the mutated form of the
// resource resulting from an Apply<type> call.
func SetSpecHashAnnotation(objMeta *metav1.ObjectMeta, spec interface{}) error {
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
			recorder.Event(required, corev1.EventTypeWarning, "Update failed", err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, "Updated successfully", "Resource was successfully updated")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Update failed", err.Error())
		return false, err
	}

	modified := resourcemerge.BoolPtr(false)
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
		recorder.Event(required, corev1.EventTypeWarning, "Update failed", err.Error())
		return false, err
	}
	recorder.Event(toWrite, corev1.EventTypeNormal, "Updated successfully", "Resource was successfully updated")
	return true, err
}

func applyDeployment(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *appsv1.Deployment) (bool, error) {
	required := requiredOriginal.DeepCopy()
	err := SetSpecHashAnnotation(&required.ObjectMeta, required.Spec)
	if err != nil {
		return false, err
	}

	existing := &appsv1.Deployment{}
	err = client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		required.Annotations[generationAnnotation] = "1"
		err := client.Create(ctx, required)
		if err != nil {
			recorder.Event(required, corev1.EventTypeWarning, "Create failed", err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, "Created successfully", "Resource was successfully updated")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Failed to get resource for update", err.Error())
		return false, err
	}

	modified := resourcemerge.BoolPtr(false)
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
		// This check need for break a recursion in case if resource deletion was stuck during deletion
		if existing.DeletionTimestamp != nil {
			return false, fmt.Errorf("resource was already marked for deletion, returning")
		}
		recorder.Event(
			existing, corev1.EventTypeNormal,
			"Delete existing deployment", "Delete existing deployment to recreate it with new parameters",
		)
		// Perform dry run creation in order to validate deployment before deleting existing one
		requiredCopy := required.DeepCopy()
		requiredCopy.Name = fmt.Sprintf("%s-dry-run", requiredCopy.Name)
		dryRunOpts := &coreclientv1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
		err = client.Create(ctx, requiredCopy, dryRunOpts)
		if err != nil {
			recorder.Event(existing, corev1.EventTypeWarning, "New resource validation failed", err.Error())
			return false, err
		}
		err = client.Delete(ctx, existing)
		if err != nil && !apierrors.IsNotFound(err) {
			recorder.Event(existing, corev1.EventTypeWarning, "Deletion failed", err.Error())
			return false, err
		}
		return applyDeployment(ctx, client, recorder, required)
	}

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = *required.Spec.DeepCopy()

	toWrite.Annotations[generationAnnotation] = fmt.Sprintf("%x", existingCopy.GetGeneration()+1)

	err = client.Update(ctx, toWrite)
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Update failed", err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, "Updated successfully", "Resource was successfully updated")
	return true, nil
}

func applyDaemonSet(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *appsv1.DaemonSet) (bool, error) {
	required := requiredOriginal.DeepCopy()
	err := SetSpecHashAnnotation(&required.ObjectMeta, required.Spec)
	if err != nil {
		return false, err
	}

	existing := &appsv1.DaemonSet{}
	err = client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		required.Annotations[generationAnnotation] = "1"
		err = client.Create(ctx, required)
		if err != nil {
			recorder.Event(required, corev1.EventTypeWarning, "Create failed", err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, "Created successfully", "Resource was successfully created")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Failed to get resource for update", err.Error())
		return false, err
	}

	modified := resourcemerge.BoolPtr(false)
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
		// This check need for break a recursion in case if resource was stuck during deletion
		if existing.DeletionTimestamp != nil {
			return false, fmt.Errorf("resource was already marked for deletion, returning")
		}
		recorder.Event(
			existing, corev1.EventTypeNormal,
			"Delete existing daemonset", "Delete existing daemonset to recreate it with new parameters",
		)
		// Perform dry run creation in order to validate ds before deleting existing one
		requiredCopy := required.DeepCopy()
		requiredCopy.Name = fmt.Sprintf("%s-dry-run", requiredCopy.Name)
		dryRunOpts := &coreclientv1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
		err = client.Create(ctx, requiredCopy, dryRunOpts)
		if err != nil {
			recorder.Event(existing, corev1.EventTypeWarning, "New resource validation failed", err.Error())
			return false, err
		}
		err = client.Delete(ctx, existing)
		if err != nil && !apierrors.IsNotFound(err) {
			recorder.Event(existing, corev1.EventTypeWarning, "Deletion failed", err.Error())
			return false, err
		}
		return applyDaemonSet(ctx, client, recorder, required)
	}

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = *required.Spec.DeepCopy()

	toWrite.Annotations[generationAnnotation] = fmt.Sprintf("%x", existingCopy.GetGeneration()+1)

	err = client.Update(ctx, toWrite)
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Update failed", err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, "Updated successfully", "Resource was successfully updated")
	return true, nil
}

func applyPodDisruptionBudget(ctx context.Context, client coreclientv1.Client, recorder record.EventRecorder, requiredOriginal *policyv1.PodDisruptionBudget) (bool, error) {
	required := requiredOriginal.DeepCopy()

	existing := &policyv1.PodDisruptionBudget{}
	err := client.Get(ctx, coreclientv1.ObjectKeyFromObject(required), existing)
	if apierrors.IsNotFound(err) {
		err = client.Create(ctx, required)
		if err != nil {
			recorder.Event(required, corev1.EventTypeWarning, "Create failed", err.Error())
			return false, err
		}
		recorder.Event(required, corev1.EventTypeNormal, "Created successfully", "Resource was successfully updated")
		return true, nil
	}
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Failed to get resource for update", err.Error())
		return false, err
	}

	modified := resourcemerge.BoolPtr(false)
	existingCopy := existing.DeepCopy()

	resourcemerge.EnsureObjectMeta(modified, &existingCopy.ObjectMeta, required.ObjectMeta)
	contentSame := equality.Semantic.DeepEqual(existingCopy.Spec, required.Spec)

	if !*modified && contentSame {
		return false, nil
	}

	// at this point we know that we're going to perform a write.  We're just trying to get the object correct
	toWrite := existingCopy // shallow copy so the code reads easier
	toWrite.Spec = *required.Spec.DeepCopy()

	err = client.Update(ctx, toWrite)
	if err != nil {
		recorder.Event(required, corev1.EventTypeWarning, "Update failed", err.Error())
		return false, err
	}
	recorder.Event(required, corev1.EventTypeNormal, "Updated successfully", "Resource was successfully updated")
	return true, nil
}
