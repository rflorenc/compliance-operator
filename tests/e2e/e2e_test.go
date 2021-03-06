package e2e

import (
	goctx "context"
	"fmt"
	"math/rand"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	compv1alpha1 "github.com/openshift/compliance-operator/pkg/apis/compliance/v1alpha1"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

func TestE2E(t *testing.T) {
	executeTests(t,
		testExecution{
			Name:       "TestProfileModification",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				const (
					baselineImage       = "quay.io/jhrozek/ocp4-openscap-content@sha256:a1709f5150b17a9560a5732fe48a89f07bffc72c0832aa8c49ee5504510ae687"
					modifiedImage       = "quay.io/jhrozek/ocp4-openscap-content@sha256:7999243c0b005792bd58c6f5e1776ca88cf20adac1519c00ef08b18e77188db7"
					removedRule         = "chronyd-no-chronyc-network"
					unlinkedRule        = "chronyd-client-only"
					moderateProfileName = "moderate"
				)

				prefixName := func(profName, ruleBaseName string) string { return profName + "-" + ruleBaseName }

				pbName := getObjNameFromTest(t)
				origPb := &compv1alpha1.ProfileBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      pbName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ProfileBundleSpec{
						ContentImage: baselineImage,
						ContentFile:  rhcosContentFile,
					},
				}
				if err := f.Client.Create(goctx.TODO(), origPb, getCleanupOpts(ctx)); err != nil {
					return err
				}
				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamValid); err != nil {
					return err
				}
				if err := assertMustHaveParsedProfiles(f, pbName, string(compv1alpha1.ScanTypeNode), "redhat_enterprise_linux_coreos_4"); err != nil {
					return err
				}

				// Check that the rule we removed exists in the original profile
				err, found := doesRuleExist(f, origPb.Namespace, prefixName(pbName, removedRule))
				if err != nil {
					return err
				} else if found != true {
					t.Errorf("Expected rule %s not found", prefixName(pbName, removedRule))
					return err
				}

				// Check that the rule we unlined in the modified profile is linked in the original
				profilePreUpdate := &compv1alpha1.Profile{}
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: origPb.Namespace, Name: prefixName(pbName, moderateProfileName)}, profilePreUpdate); err != nil {
					return err
				}
				found = findRuleReference(profilePreUpdate, prefixName(pbName, unlinkedRule))
				if found != true {
					t.Errorf("Expected rule %s not found", prefixName(pbName, unlinkedRule))
					return err
				}

				// update the image with a new hash
				modPb := origPb.DeepCopy()
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: modPb.Namespace, Name: modPb.Name}, modPb); err != nil {
					return err
				}

				modPb.Spec.ContentImage = modifiedImage
				if err := f.Client.Update(goctx.TODO(), modPb); err != nil {
					return err
				}

				// Wait for the update to happen, the PB will flip first to pending, then to valid
				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamPending); err != nil {
					return err
				}
				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamValid); err != nil {
					return err
				}

				if err := assertMustHaveParsedRules(t, f, namespace, pbName); err != nil {
					return err
				}

				// We removed this rule in the update, is must no longer exist
				err, found = doesRuleExist(f, origPb.Namespace, prefixName(pbName, removedRule))
				if err != nil {
					return err
				} else if found != false {
					t.Errorf("Rule %s unexpectedly found", prefixName(pbName, removedRule))
					return err
				}

				// This rule was unlinked
				profilePostUpdate := &compv1alpha1.Profile{}
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: origPb.Namespace, Name: prefixName(pbName, moderateProfileName)}, profilePostUpdate); err != nil {
					return err
				}
				found = findRuleReference(profilePostUpdate, prefixName(pbName, unlinkedRule))
				if found != false {
					t.Errorf("Rule %s unexpectedly found", prefixName(pbName, unlinkedRule))
					return err
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestProfileISTagUpdate",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				const (
					baselineImage       = "quay.io/jhrozek/ocp4-openscap-content@sha256:a1709f5150b17a9560a5732fe48a89f07bffc72c0832aa8c49ee5504510ae687"
					modifiedImageDigest = "sha256:7999243c0b005792bd58c6f5e1776ca88cf20adac1519c00ef08b18e77188db7"
					removedRule         = "chronyd-no-chronyc-network"
					unlinkedRule        = "chronyd-client-only"
					moderateProfileName = "moderate"
				)
				var (
					modifiedImage = fmt.Sprintf("quay.io/jhrozek/ocp4-openscap-content@%s", modifiedImageDigest)
				)

				prefixName := func(profName, ruleBaseName string) string { return profName + "-" + ruleBaseName }

				pbName := getObjNameFromTest(t)
				iSName := pbName

				if err := createImageStream(f, ctx, iSName, namespace, baselineImage); err != nil {
					return err
				}

				pb := &compv1alpha1.ProfileBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      pbName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ProfileBundleSpec{
						ContentImage: fmt.Sprintf("%s:%s", iSName, "latest"),
						ContentFile:  rhcosContentFile,
					},
				}

				if err := f.Client.Create(goctx.TODO(), pb, getCleanupOpts(ctx)); err != nil {
					return err
				}

				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamValid); err != nil {
					return err
				}
				if err := assertMustHaveParsedProfiles(f, pbName, string(compv1alpha1.ScanTypeNode), "redhat_enterprise_linux_coreos_4"); err != nil {
					return err
				}

				// Check that the rule we removed exists in the original profile
				err, found := doesRuleExist(f, pb.Namespace, prefixName(pbName, removedRule))
				if err != nil {
					return err
				} else if found != true {
					t.Errorf("Expected rule %s not found", prefixName(pbName, removedRule))
					return err
				}

				// Check that the rule we unlined in the modified profile is linked in the original
				profilePreUpdate := &compv1alpha1.Profile{}
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: pb.Namespace, Name: prefixName(pbName, moderateProfileName)}, profilePreUpdate); err != nil {
					return err
				}
				found = findRuleReference(profilePreUpdate, prefixName(pbName, unlinkedRule))
				if found != true {
					t.Errorf("Expected rule %s not found", prefixName(pbName, unlinkedRule))
					return err
				}

				// Update the reference in the image stream
				if err := updateImageStreamTag(f, iSName, namespace, modifiedImage); err != nil {
					return err
				}

				// Note that when an update happens through an imagestream tag, the operator doesn't get
				// a notification about it... It all happens on the Kube Deployment's side.
				// So we don't need to wait for the profile bundle's statuses
				if err := waitForDeploymentContentUpdate(t, f, namespace, pbName, modifiedImageDigest); err != nil {
					return err
				}

				if err := assertMustHaveParsedRules(t, f, namespace, pbName); err != nil {
					return err
				}

				// We removed this rule in the update, it must no longer exist
				err, found = doesRuleExist(f, pb.Namespace, prefixName(pbName, removedRule))
				if err != nil {
					return err
				} else if found != false {
					t.Errorf("Rule %s unexpectedly found", prefixName(pbName, removedRule))
					return err
				}

				// This rule was unlinked
				profilePostUpdate := &compv1alpha1.Profile{}
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: pb.Namespace, Name: prefixName(pbName, moderateProfileName)}, profilePostUpdate); err != nil {
					return err
				}
				found = findRuleReference(profilePostUpdate, prefixName(pbName, unlinkedRule))
				if found != false {
					t.Errorf("Rule %s unexpectedly found", prefixName(pbName, unlinkedRule))
					return err
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestProfileISTagOtherNs",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				const (
					baselineImage       = "quay.io/jhrozek/ocp4-openscap-content@sha256:a1709f5150b17a9560a5732fe48a89f07bffc72c0832aa8c49ee5504510ae687"
					modifiedImageDigest = "sha256:7999243c0b005792bd58c6f5e1776ca88cf20adac1519c00ef08b18e77188db7"
					removedRule         = "chronyd-no-chronyc-network"
					unlinkedRule        = "chronyd-client-only"
					moderateProfileName = "moderate"
				)
				var (
					modifiedImage = fmt.Sprintf("quay.io/jhrozek/ocp4-openscap-content@%s", modifiedImageDigest)
				)

				prefixName := func(profName, ruleBaseName string) string { return profName + "-" + ruleBaseName }

				pbName := getObjNameFromTest(t)
				iSName := pbName
				otherNs := "openshift"

				if err := createImageStream(f, ctx, iSName, otherNs, baselineImage); err != nil {
					return err
				}

				pb := &compv1alpha1.ProfileBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      pbName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ProfileBundleSpec{
						ContentImage: fmt.Sprintf("%s/%s:%s", otherNs, iSName, "latest"),
						ContentFile:  rhcosContentFile,
					},
				}

				if err := f.Client.Create(goctx.TODO(), pb, getCleanupOpts(ctx)); err != nil {
					return err
				}

				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamValid); err != nil {
					return err
				}
				if err := assertMustHaveParsedProfiles(f, pbName, string(compv1alpha1.ScanTypeNode), "redhat_enterprise_linux_coreos_4"); err != nil {
					return err
				}

				// Check that the rule we removed exists in the original profile
				err, found := doesRuleExist(f, pb.Namespace, prefixName(pbName, removedRule))
				if err != nil {
					return err
				} else if found != true {
					t.Errorf("Expected rule %s not found", prefixName(pbName, removedRule))
					return err
				}

				// Check that the rule we unlined in the modified profile is linked in the original
				profilePreUpdate := &compv1alpha1.Profile{}
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: pb.Namespace, Name: prefixName(pbName, moderateProfileName)}, profilePreUpdate); err != nil {
					return err
				}
				found = findRuleReference(profilePreUpdate, prefixName(pbName, unlinkedRule))
				if found != true {
					t.Errorf("Expected rule %s not found", prefixName(pbName, unlinkedRule))
					return err
				}

				// Update the reference in the image stream
				if err := updateImageStreamTag(f, iSName, otherNs, modifiedImage); err != nil {
					return err
				}

				// Note that when an update happens through an imagestream tag, the operator doesn't get
				// a notification about it... It all happens on the Kube Deployment's side.
				// So we don't need to wait for the profile bundle's statuses
				if err := waitForDeploymentContentUpdate(t, f, namespace, pbName, modifiedImageDigest); err != nil {
					return err
				}

				if err := assertMustHaveParsedRules(t, f, namespace, pbName); err != nil {
					return err
				}

				// We removed this rule in the update, it must no longer exist
				err, found = doesRuleExist(f, pb.Namespace, prefixName(pbName, removedRule))
				if err != nil {
					return err
				} else if found != false {
					t.Errorf("Rule %s unexpectedly found", prefixName(pbName, removedRule))
					return err
				}

				// This rule was unlinked
				profilePostUpdate := &compv1alpha1.Profile{}
				if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: pb.Namespace, Name: prefixName(pbName, moderateProfileName)}, profilePostUpdate); err != nil {
					return err
				}
				found = findRuleReference(profilePostUpdate, prefixName(pbName, unlinkedRule))
				if found != false {
					t.Errorf("Rule %s unexpectedly found", prefixName(pbName, unlinkedRule))
					return err
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestInvalidBundleWithUnexistentRef",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				const (
					unexistentImage     = "bad-namespace/bad-image:latest"
					removedRule         = "chronyd-no-chronyc-network"
					unlinkedRule        = "chronyd-client-only"
					moderateProfileName = "moderate"
				)

				pbName := getObjNameFromTest(t)

				pb := &compv1alpha1.ProfileBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      pbName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ProfileBundleSpec{
						ContentImage: unexistentImage,
						ContentFile:  rhcosContentFile,
					},
				}

				if err := f.Client.Create(goctx.TODO(), pb, getCleanupOpts(ctx)); err != nil {
					return err
				}

				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamInvalid); err != nil {
					return err
				}
				return nil
			},
		},
		testExecution{
			Name:       "TestInvalidBundleWithNoTag",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				const (
					noTagImage          = "bad-namespace/bad-image"
					removedRule         = "chronyd-no-chronyc-network"
					unlinkedRule        = "chronyd-client-only"
					moderateProfileName = "moderate"
				)

				pbName := getObjNameFromTest(t)

				pb := &compv1alpha1.ProfileBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      pbName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ProfileBundleSpec{
						ContentImage: noTagImage,
						ContentFile:  rhcosContentFile,
					},
				}

				if err := f.Client.Create(goctx.TODO(), pb, getCleanupOpts(ctx)); err != nil {
					return err
				}

				if err := waitForProfileBundleStatus(t, f, namespace, pbName, compv1alpha1.DataStreamInvalid); err != nil {
					return err
				}
				return nil
			},
		},
		testExecution{
			Name:       "TestSingleScanSucceeds",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				scanName := getObjNameFromTest(t)
				testScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				err = scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}
				return scanHasValidPVCReference(f, namespace, scanName)
			},
		},
		testExecution{
			Name:       "TestScanProducesRemediations",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				scanName := getObjNameFromTest(t)
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}
				testScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
						NodeSelector: selectWorkers,
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				// We expect that a scan that is using all the rules wouldn't be compliant
				err = scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// Since the scan was not compliant, there should be some remediations and none
				// of them should be an error
				inNs := client.InNamespace(namespace)
				withLabel := client.MatchingLabels{compv1alpha1.ComplianceScanLabel: testScan.Name}
				fmt.Println(inNs, withLabel)
				remList := &compv1alpha1.ComplianceRemediationList{}
				err = f.Client.List(goctx.TODO(), remList, inNs, withLabel)
				if err != nil {
					return err
				}

				if len(remList.Items) == 0 {
					return fmt.Errorf("expected at least one remediation")
				}
				for _, rem := range remList.Items {
					if rem.Status.ApplicationState != compv1alpha1.RemediationNotApplied {
						return fmt.Errorf("expected all remediations are unapplied when scan finishes")
					}
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestSingleScanWithStorageSucceeds",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				scanName := getObjNameFromTest(t)
				testScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							RawResultStorage: compv1alpha1.RawResultStorageSettings{
								Size: "2Gi",
							},
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				err = scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}
				return scanHasValidPVCReferenceWithSize(f, namespace, scanName, "2Gi")
			},
		},
		testExecution{
			Name:       "TestScanStorageOutOfLimitRangeFails",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				// Create LimitRange
				lr := &corev1.LimitRange{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-limitrange",
						Namespace: namespace,
					},
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePersistentVolumeClaim,
								Max: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("5Gi"),
								},
							},
						},
					},
				}
				if err := f.Client.Create(goctx.TODO(), lr, getCleanupOpts(ctx)); err != nil {
					return err
				}

				scanName := getObjNameFromTest(t)
				testScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							RawResultStorage: compv1alpha1.RawResultStorageSettings{
								Size: "6Gi",
							},
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				err = scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultError)
				if err != nil {
					return err
				}

				// Clean up limitrange
				if err := f.Client.Delete(goctx.TODO(), lr); err != nil {
					return err
				}
				return nil
			},
		},
		testExecution{
			Name: "TestScanStorageOutOfQuotaRangeFails",
			// This can't be parallel since it's a global quota for the namespace
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				// Create ResourceQuota
				rq := &corev1.ResourceQuota{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-resourcequota",
						Namespace: namespace,
					},
					Spec: corev1.ResourceQuotaSpec{
						Hard: corev1.ResourceList{
							corev1.ResourceRequestsStorage: resource.MustParse("5Gi"),
						},
					},
				}
				if err := f.Client.Create(goctx.TODO(), rq, getCleanupOpts(ctx)); err != nil {
					return err
				}

				scanName := getObjNameFromTest(t)
				testScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							RawResultStorage: compv1alpha1.RawResultStorageSettings{
								Size: "6Gi",
							},
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				err = scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultError)
				if err != nil {
					return err
				}
				// delete resource quota
				if err := f.Client.Delete(goctx.TODO(), rq); err != nil {
					return err
				}
				return nil
			},
		},
		testExecution{
			Name:       "TestSingleTailoredScanSucceeds",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				tailoringCM := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-single-tailored-scan-succeeds-cm",
						Namespace: namespace,
					},
					Data: map[string]string{
						"tailoring.xml": `<?xml version="1.0" encoding="UTF-8"?>
<xccdf-1.2:Tailoring xmlns:xccdf-1.2="http://checklists.nist.gov/xccdf/1.2" id="xccdf_compliance.openshift.io_tailoring_test-tailoredprofile">
	<xccdf-1.2:benchmark href="/content/ssg-rhcos4-ds.xml"></xccdf-1.2:benchmark>
	<xccdf-1.2:version time="2020-04-28T07:04:13Z">1</xccdf-1.2:version>
	<xccdf-1.2:Profile id="xccdf_compliance.openshift.io_profile_test-tailoredprofile">
		<xccdf-1.2:title>Test Tailored Profile</xccdf-1.2:title>
		<xccdf-1.2:description>Test Tailored Profile</xccdf-1.2:description>
		<xccdf-1.2:select idref="xccdf_org.ssgproject.content_rule_no_netrc_files" selected="true"></xccdf-1.2:select>
	</xccdf-1.2:Profile>
</xccdf-1.2:Tailoring>`,
					},
				}

				err := f.Client.Create(goctx.TODO(), tailoringCM, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-single-tailored-scan-succeeds",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_compliance.openshift.io_profile_test-tailoredprofile",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						TailoringConfigMap: &compv1alpha1.TailoringConfigMapRef{
							Name: tailoringCM.Name,
						},
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err = f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-single-tailored-scan-succeeds", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				return scanResultIsExpected(t, f, namespace, "test-single-tailored-scan-succeeds", compv1alpha1.ResultCompliant)
			},
		},
		testExecution{
			Name:       "TestScanWithNodeSelectorFiltersCorrectly",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}
				testComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-filtered-scan",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile:      "xccdf_org.ssgproject.content_profile_moderate",
						Content:      rhcosContentFile,
						Rule:         "xccdf_org.ssgproject.content_rule_no_netrc_files",
						NodeSelector: selectWorkers,
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-filtered-scan", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				nodes := getNodesWithSelector(f, selectWorkers)
				configmaps := getConfigMapsFromScan(f, testComplianceScan)
				if len(nodes) != len(configmaps) {
					return fmt.Errorf(
						"The number of reports doesn't match the number of selected nodes: "+
							"%d reports / %d nodes", len(configmaps), len(nodes))
				}
				return scanResultIsExpected(t, f, namespace, "test-filtered-scan", compv1alpha1.ResultCompliant)
			},
		},
		testExecution{
			Name:       "TestScanWithNodeSelectorNoMatches",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				scanName := getObjNameFromTest(t)
				selectNone := map[string]string{
					"node-role.kubernetes.io/no-matches": "",
				}
				testComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile:      "xccdf_org.ssgproject.content_profile_moderate",
						Content:      rhcosContentFile,
						Rule:         "xccdf_org.ssgproject.content_rule_no_netrc_files",
						NodeSelector: selectNone,
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultNotApplicable)
			},
		},
		testExecution{
			Name:       "TestScanWithInvalidScanTypeFails",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				scanName := getObjNameFromTest(t)
				testScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile:  "xccdf_org.ssgproject.content_profile_moderate",
						Content:  "ssg-ocp4-non-existent.xml",
						ScanType: "BadScanType",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultError)
			},
		},
		testExecution{
			Name:       "TestScanWithInvalidContentFails",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-scan-w-invalid-content",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: "ssg-ocp4-non-existent.xml",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-scan-w-invalid-content", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, "test-scan-w-invalid-content", compv1alpha1.ResultError)
			},
		},
		testExecution{
			Name:       "TestScanWithInvalidProfileFails",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-scan-w-invalid-profile",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_coreos-unexistent",
						Content: rhcosContentFile,
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-scan-w-invalid-profile", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, "test-scan-w-invalid-profile", compv1alpha1.ResultError)
			},
		},
		testExecution{
			Name:       "TestMalformedTailoredScanFails",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				tailoringCM := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-malformed-tailored-scan-fails-cm",
						Namespace: namespace,
					},
					// The tailored profile's namespace is wrong. It should be xccdf-1.2, but it was
					// declared as xccdf. So it should report an error
					Data: map[string]string{
						"tailoring.xml": `<?xml version="1.0" encoding="UTF-8"?>
<xccdf-1.2:Tailoring xmlns:xccdf="http://checklists.nist.gov/xccdf/1.2" id="xccdf_compliance.openshift.io_tailoring_test-tailoredprofile">
	<xccdf-1.2:benchmark href="/content/ssg-rhcos4-ds.xml"></xccdf-1.2:benchmark>
	<xccdf-1.2:version time="2020-04-28T07:04:13Z">1</xccdf-1.2:version>
	<xccdf-1.2:Profile id="xccdf_compliance.openshift.io_profile_test-tailoredprofile">
		<xccdf-1.2:title>Test Tailored Profile</xccdf-1.2:title>
		<xccdf-1.2:description>Test Tailored Profile</xccdf-1.2:description>
		<xccdf-1.2:select idref="xccdf_org.ssgproject.content_rule_no_netrc_files" selected="true"></xccdf-1.2:select>
	</xccdf-1.2:Profile>
</xccdf-1.2:Tailoring>`,
					},
				}

				err := f.Client.Create(goctx.TODO(), tailoringCM, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-malformed-tailored-scan-fails",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_compliance.openshift.io_profile_test-tailoredprofile",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
						TailoringConfigMap: &compv1alpha1.TailoringConfigMapRef{
							Name: tailoringCM.Name,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err = f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-malformed-tailored-scan-fails", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, "test-malformed-tailored-scan-fails", compv1alpha1.ResultError)
			},
		},
		testExecution{
			Name:       "TestScanWithEmptyTailoringCMNameFails",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-scan-w-empty-tailoring-cm",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						TailoringConfigMap: &compv1alpha1.TailoringConfigMapRef{
							Name: "",
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-scan-w-empty-tailoring-cm", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, "test-scan-w-empty-tailoring-cm", compv1alpha1.ResultError)
			},
		},
		testExecution{
			Name:       "TestScanWithMissingTailoringCMFailsAndRecovers",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				scanName := "test-scan-w-missing-tailoring-cm"
				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_compliance.openshift.io_profile_test-tailoredprofile",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
						TailoringConfigMap: &compv1alpha1.TailoringConfigMapRef{
							Name: "missing-tailoring-file",
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseLaunching)
				if err != nil {
					return err
				}

				var resultErr error
				// The status might still be NOT-AVAILABLE... we can wait a bit
				// for the reconciliation to update it.
				_ = wait.PollImmediate(retryInterval, timeout, func() (bool, error) {
					if resultErr = scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultError); resultErr != nil {
						return false, nil
					}
					return true, nil
				})
				if resultErr != nil {
					return resultErr
				}

				tailoringCM := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "missing-tailoring-file",
						Namespace: namespace,
					},
					Data: map[string]string{
						"tailoring.xml": `<?xml version="1.0" encoding="UTF-8"?>
<xccdf-1.2:Tailoring xmlns:xccdf-1.2="http://checklists.nist.gov/xccdf/1.2" id="xccdf_compliance.openshift.io_tailoring_test-tailoredprofile">
	<xccdf-1.2:benchmark href="/content/ssg-rhcos4-ds.xml"></xccdf-1.2:benchmark>
	<xccdf-1.2:version time="2020-04-28T07:04:13Z">1</xccdf-1.2:version>
	<xccdf-1.2:Profile id="xccdf_compliance.openshift.io_profile_test-tailoredprofile">
		<xccdf-1.2:title>Test Tailored Profile</xccdf-1.2:title>
		<xccdf-1.2:description>Test Tailored Profile</xccdf-1.2:description>
		<xccdf-1.2:select idref="xccdf_org.ssgproject.content_rule_no_netrc_files" selected="true"></xccdf-1.2:select>
	</xccdf-1.2:Profile>
</xccdf-1.2:Tailoring>`,
					},
				}
				err = f.Client.Create(goctx.TODO(), tailoringCM, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				err = waitForScanStatus(t, f, namespace, scanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				return scanResultIsExpected(t, f, namespace, scanName, compv1alpha1.ResultCompliant)
			},
		},
		testExecution{
			Name:       "TestMissingPodInRunningState",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				exampleComplianceScan := &compv1alpha1.ComplianceScan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-missing-pod-scan",
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceScanSpec{
						Profile: "xccdf_org.ssgproject.content_profile_moderate",
						Content: rhcosContentFile,
						Rule:    "xccdf_org.ssgproject.content_rule_no_netrc_files",
						ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
							Debug: true,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), exampleComplianceScan, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-missing-pod-scan", compv1alpha1.PhaseRunning)
				if err != nil {
					return err
				}
				pods, err := getPodsForScan(f, "test-missing-pod-scan")
				if err != nil {
					return err
				}
				if len(pods) < 1 {
					return fmt.Errorf("No pods gotten from query for the scan")
				}
				podToDelete := pods[rand.Intn(len(pods))]
				// Delete pod ASAP
				zeroSeconds := int64(0)
				do := client.DeleteOptions{GracePeriodSeconds: &zeroSeconds}
				err = f.Client.Delete(goctx.TODO(), &podToDelete, &do)
				if err != nil {
					return err
				}
				err = waitForScanStatus(t, f, namespace, "test-missing-pod-scan", compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}
				return scanResultIsExpected(t, f, namespace, "test-missing-pod-scan", compv1alpha1.ResultCompliant)
			},
		},
		testExecution{
			Name:       "TestApplyGenericRemediation",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				remName := "test-apply-generic-remediation"
				unstruct := &unstructured.Unstructured{}
				unstruct.SetUnstructuredContent(map[string]interface{}{
					"kind":       "ConfigMap",
					"apiVersion": "v1",
					"metadata": map[string]interface{}{
						"name":      "generic-rem-cm",
						"namespace": namespace,
					},
					"data": map[string]interface{}{
						"key": "value",
					},
				})

				genericRem := &compv1alpha1.ComplianceRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      remName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceRemediationSpec{
						ComplianceRemediationSpecMeta: compv1alpha1.ComplianceRemediationSpecMeta{
							Apply: true,
						},
						Current: compv1alpha1.ComplianceRemediationPayload{
							Object: unstruct,
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), genericRem, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForRemediationState(t, f, namespace, remName, compv1alpha1.RemediationApplied)
				if err != nil {
					return err
				}

				cm := &corev1.ConfigMap{}
				err = waitForObjectToExist(t, f, "generic-rem-cm", namespace, cm)
				if err != nil {
					return err
				}
				val, ok := cm.Data["key"]
				if !ok || val != "value" {
					return fmt.Errorf("ComplianceRemediation '%s' generated a malformed ConfigMap", remName)
				}
				return nil
			},
		},
		testExecution{
			Name:       "TestGenericRemediationFailsWithUnkownType",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				remName := "test-generic-remediation-fails-unkown"
				genericRem := &compv1alpha1.ComplianceRemediation{
					ObjectMeta: metav1.ObjectMeta{
						Name:      remName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceRemediationSpec{
						ComplianceRemediationSpecMeta: compv1alpha1.ComplianceRemediationSpecMeta{
							Apply: true,
						},
						Current: compv1alpha1.ComplianceRemediationPayload{
							Object: &unstructured.Unstructured{
								Object: map[string]interface{}{
									"kind":       "OopsyDoodle",
									"apiVersion": "foo.bar/v1",
									"metadata": map[string]interface{}{
										"name":      "unkown-remediation",
										"namespace": namespace,
									},
									"data": map[string]interface{}{
										"key": "value",
									},
								},
							},
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), genericRem, getCleanupOpts(ctx))
				if err != nil {
					return err
				}
				err = waitForRemediationState(t, f, namespace, remName, compv1alpha1.RemediationError)
				if err != nil {
					return err
				}
				return nil
			},
		},
		testExecution{
			Name:       "TestSuiteWithInvalidScheduleShowsError",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				suiteName := "test-suite-with-invalid-schedule"
				testSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
							Schedule:              "This is WRONG",
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								Name: fmt.Sprintf("%s-workers-scan", suiteName),
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
									NodeSelector: map[string]string{
										"node-role.kubernetes.io/worker": "",
									},
								},
							},
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultError)
				if err != nil {
					return err
				}
				return suiteErrorMessageMatchesRegex(t, f, namespace, suiteName, "Suite was invalid: .*")
			},
		},
		testExecution{
			Name:       "TestSuiteScan",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				suiteName := "test-suite-two-scans"

				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}

				masterScanName := fmt.Sprintf("%s-masters-scan", suiteName)
				selectMasters := map[string]string{
					"node-role.kubernetes.io/master": "",
				}

				exampleComplianceSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									NodeSelector: selectWorkers,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: workerScanName,
							},
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									NodeSelector: selectMasters,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: masterScanName,
							},
						},
					},
				}

				err := f.Client.Create(goctx.TODO(), exampleComplianceSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// At this point, both scans should be non-compliant given our current content
				err = scanResultIsExpected(t, f, namespace, workerScanName, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}
				err = scanResultIsExpected(t, f, namespace, masterScanName, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// Each scan should produce two remediations
				workerRemediations := []string{
					fmt.Sprintf("%s-no-empty-passwords", workerScanName),
					fmt.Sprintf("%s-no-direct-root-logins", workerScanName),
				}
				err = assertHasRemediations(t, f, suiteName, workerScanName, "worker", workerRemediations)
				if err != nil {
					return err
				}

				masterRemediations := []string{
					fmt.Sprintf("%s-no-empty-passwords", masterScanName),
					fmt.Sprintf("%s-no-direct-root-logins", masterScanName),
				}
				err = assertHasRemediations(t, f, suiteName, masterScanName, "master", masterRemediations)
				if err != nil {
					return err
				}

				checkWifiInBios := compv1alpha1.ComplianceCheckResult{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-wireless-disable-in-bios", workerScanName),
						Namespace: namespace,
					},
					ID:       "xccdf_org.ssgproject.content_rule_wireless_disable_in_bios",
					Status:   compv1alpha1.CheckResultInfo,
					Severity: compv1alpha1.CheckResultSeverityUnknown, // yes, it's really uknown in the DS
				}

				err = assertHasCheck(f, suiteName, workerScanName, checkWifiInBios)
				if err != nil {
					return err
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestScheduledSuite",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				suiteName := "test-scheduled-suite"

				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}

				testSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
							Schedule:              "*/2 * * * *",
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								Name: workerScanName,
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									Rule:         "xccdf_org.ssgproject.content_rule_no_netrc_files",
									NodeSelector: selectWorkers,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										RawResultStorage: compv1alpha1.RawResultStorageSettings{
											Rotation: 1,
										},
										Debug: true,
									},
								},
							},
						},
					},
				}

				err := f.Client.Create(goctx.TODO(), testSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}

				// Wait for one re-scan
				err = waitForReScanStatus(t, f, namespace, workerScanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				// Wait for a second one to assert this is running scheduled as expected
				err = waitForReScanStatus(t, f, namespace, workerScanName, compv1alpha1.PhaseDone)
				if err != nil {
					return err
				}

				// clean up
				// Get new reference of suite
				foundSuite := &compv1alpha1.ComplianceSuite{}
				key := types.NamespacedName{Name: testSuite.Name, Namespace: testSuite.Namespace}
				if err = f.Client.Get(goctx.TODO(), key, foundSuite); err != nil {
					return err
				}

				// Remove cronjob so it doesn't keep running while other tests are running
				testSuiteCopy := foundSuite.DeepCopy()
				updatedSchedule := ""
				testSuiteCopy.Spec.Schedule = updatedSchedule
				if err = f.Client.Update(goctx.TODO(), testSuiteCopy); err != nil {
					return err
				}

				rawResultClaimName, err := getRawResultClaimNameFromScan(t, f, namespace, workerScanName)
				if err != nil {
					return err
				}

				rotationCheckerPod := getRotationCheckerWorkload(namespace, rawResultClaimName)
				if err = f.Client.Create(goctx.TODO(), rotationCheckerPod, getCleanupOpts(ctx)); err != nil {
					return err
				}

				return assertResultStorageHasExpectedItemsAfterRotation(t, f, 1, namespace, rotationCheckerPod.Name)
			},
		},
		testExecution{
			Name:       "TestScheduledSuiteUpdate",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				suiteName := getObjNameFromTest(t)
				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}

				initialSchedule := "0 * * * *"
				testSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
							Schedule:              initialSchedule,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								Name: workerScanName,
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									Rule:         "xccdf_org.ssgproject.content_rule_no_netrc_files",
									NodeSelector: selectWorkers,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
							},
						},
					},
				}

				err := f.Client.Create(goctx.TODO(), testSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}

				err = waitForCronJobWithSchedule(t, f, namespace, suiteName, initialSchedule)
				if err != nil {
					return err
				}

				// Get new reference of suite
				foundSuite := &compv1alpha1.ComplianceSuite{}
				key := types.NamespacedName{Name: testSuite.Name, Namespace: testSuite.Namespace}
				if err = f.Client.Get(goctx.TODO(), key, foundSuite); err != nil {
					return err
				}

				// Update schedule
				testSuiteCopy := foundSuite.DeepCopy()
				updatedSchedule := "*/2 * * * *"
				testSuiteCopy.Spec.Schedule = updatedSchedule
				if err = f.Client.Update(goctx.TODO(), testSuiteCopy); err != nil {
					return err
				}

				if err = waitForCronJobWithSchedule(t, f, namespace, suiteName, updatedSchedule); err != nil {
					return err
				}

				// Clean up
				// Get new reference of suite
				foundSuite = &compv1alpha1.ComplianceSuite{}
				if err = f.Client.Get(goctx.TODO(), key, foundSuite); err != nil {
					return err
				}

				// Remove cronjob so it doesn't keep running while other tests are running
				testSuiteCopy = foundSuite.DeepCopy()
				updatedSchedule = ""
				testSuiteCopy.Spec.Schedule = updatedSchedule
				if err = f.Client.Update(goctx.TODO(), testSuiteCopy); err != nil {
					return err
				}
				return nil
			},
		},
		testExecution{
			Name:       "TestSuiteWithContentThatDoesNotMatch",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				suiteName := "test-suite-with-non-matching-content"
				testSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								Name: fmt.Sprintf("%s-workers-scan", suiteName),
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: "quay.io/jhrozek/ocp4-openscap-content:broken_os_detection",
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      "ssg-rhcos4-ds.xml",
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
									NodeSelector: map[string]string{
										"node-role.kubernetes.io/worker": "",
									},
								},
							},
						},
					},
				}
				// use Context's create helper to create the object and add a cleanup function for the new object
				err := f.Client.Create(goctx.TODO(), testSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultNotApplicable)
				if err != nil {
					return err
				}
				return suiteErrorMessageMatchesRegex(t, f, namespace, suiteName, "The suite result is not applicable.*")
			},
		},
		testExecution{
			Name:       "TestTolerations",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				workerNodes := getNodesWithSelector(f, map[string]string{
					"node-role.kubernetes.io/worker": "",
				})

				taintedNode := &workerNodes[0]
				taintKey := "co-e2e"
				taintVal := "val"
				taint := corev1.Taint{
					Key:    taintKey,
					Value:  taintVal,
					Effect: corev1.TaintEffectNoSchedule,
				}
				if err := taintNode(t, f, taintedNode, taint); err != nil {
					E2ELog(t, "Tainting node failed")
					return err
				}
				suiteName := getObjNameFromTest(t)
				scanName := suiteName
				suite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_no_netrc_files",
									Content:      rhcosContentFile,
									NodeSelector: map[string]string{
										// Schedule scan in this specific host
										corev1.LabelHostname: taintedNode.Labels[corev1.LabelHostname],
									},
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
										ScanTolerations: []corev1.Toleration{
											{
												Key:      taintKey,
												Operator: corev1.TolerationOpExists,
												Effect:   corev1.TaintEffectNoSchedule,
											},
										},
									},
								},
								Name: scanName,
							},
						},
					},
				}
				if err := f.Client.Create(goctx.TODO(), suite, getCleanupOpts(ctx)); err != nil {
					return err
				}

				err := waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}
				return removeNodeTaint(t, f, taintedNode.Name, taintKey)
			},
		},
		testExecution{
			Name:       "TestNodeSchedulingErrorFailsTheScan",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				workerNodesLabel := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}
				workerNodes := getNodesWithSelector(f, workerNodesLabel)

				taintedNode := &workerNodes[0]
				taintKey := "co-e2e"
				taintVal := "val"
				taint := corev1.Taint{
					Key:    taintKey,
					Value:  taintVal,
					Effect: corev1.TaintEffectNoSchedule,
				}
				if err := taintNode(t, f, taintedNode, taint); err != nil {
					E2ELog(t, "Tainting node failed")
					return err
				}
				suiteName := getObjNameFromTest(t)
				scanName := suiteName
				suite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_no_netrc_files",
									Content:      rhcosContentFile,
									NodeSelector: workerNodesLabel,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: scanName,
							},
						},
					},
				}
				if err := f.Client.Create(goctx.TODO(), suite, getCleanupOpts(ctx)); err != nil {
					return err
				}

				err := waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultError)
				if err != nil {
					return err
				}
				return removeNodeTaint(t, f, taintedNode.Name, taintKey)
			},
		},
		testExecution{
			Name:       "TestScanSettingBinding",
			IsParallel: true,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				objName := getObjNameFromTest(t)

				rhcos4e8profile := &compv1alpha1.Profile{}
				key := types.NamespacedName{Namespace: namespace, Name: rhcosPb.Name + "-e8"}
				if err := f.Client.Get(goctx.TODO(), key, rhcos4e8profile); err != nil {
					return err
				}

				scanSettingName := objName + "-setting"
				scanSetting := compv1alpha1.ScanSetting{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanSettingName,
						Namespace: namespace,
					},
					ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
						AutoApplyRemediations: false,
					},
					ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
						Debug: true,
					},
					Roles: []string{"master", "worker"},
				}

				if err := f.Client.Create(goctx.TODO(), &scanSetting, getCleanupOpts(ctx)); err != nil {
					return err
				}

				scanSettingBindingName := "generated-suite"
				scanSettingBinding := compv1alpha1.ScanSettingBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scanSettingBindingName,
						Namespace: namespace,
					},
					Profiles: []compv1alpha1.NamedObjectReference{
						// TODO: test also OCP profile when it works completely
						{
							Name:     rhcos4e8profile.Name,
							Kind:     "Profile",
							APIGroup: "compliance.openshift.io/v1alpha1",
						},
					},
					SettingsRef: &compv1alpha1.NamedObjectReference{
						Name:     scanSetting.Name,
						Kind:     "ScanSetting",
						APIGroup: "compliance.openshift.io/v1alpha1",
					},
				}

				if err := f.Client.Create(goctx.TODO(), &scanSettingBinding, getCleanupOpts(ctx)); err != nil {
					return err
				}

				// Wait until the suite finishes, thus verifying the suite exists
				if err := waitForSuiteScansStatus(t, f, namespace, scanSettingBindingName, compv1alpha1.PhaseDone, compv1alpha1.ResultNonCompliant); err != nil {
					return err
				}

				masterScanKey := types.NamespacedName{Namespace: namespace, Name: rhcos4e8profile.Name + "-master"}
				masterScan := &compv1alpha1.ComplianceScan{}
				if err := f.Client.Get(goctx.TODO(), masterScanKey, masterScan); err != nil {
					return err
				}

				if masterScan.Spec.Debug != true {
					t.Errorf("Expected that the settings set debug to true in master scan")
				}

				workerScanKey := types.NamespacedName{Namespace: namespace, Name: rhcos4e8profile.Name + "-worker"}
				workerScan := &compv1alpha1.ComplianceScan{}
				if err := f.Client.Get(goctx.TODO(), workerScanKey, workerScan); err != nil {
					return err
				}

				if workerScan.Spec.Debug != true {
					t.Errorf("Expected that the settings set debug to true in workers scan")
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestAutoRemediate",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				// FIXME, maybe have a func that returns a struct with suite name and scan names?
				suiteName := "test-remediate"
				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)

				exampleComplianceSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: true,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_no_direct_root_logins",
									Content:      rhcosContentFile,
									NodeSelector: getPoolNodeRoleSelector(),
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: workerScanName,
							},
						},
					},
				}

				err := mcTctx.createE2EPool()
				if err != nil {
					t.Errorf("Cannot create subpool for this test")
					return err
				}

				err = f.Client.Create(goctx.TODO(), exampleComplianceSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Get the MachineConfigPool before a scan or remediation has been applied
				// This way, we can check that it changed without race-conditions
				poolBeforeRemediation := &mcfgv1.MachineConfigPool{}
				err = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: testPoolName}, poolBeforeRemediation)
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// We need to check that the remediation is auto-applied and save
				// the object so we can delete it later
				workersNoRootLoginsRemName := fmt.Sprintf("%s-no-direct-root-logins", workerScanName)
				err = waitForRemediationToBeAutoApplied(t, f, workersNoRootLoginsRemName, namespace, poolBeforeRemediation)
				if err != nil {
					t.Errorf("Failed to wait for nodes to come back up after applying MC: %v", err)
					return err
				}

				// We can re-run the scan at this moment and check that it's now compliant
				// and it's reflected in a CheckResult
				err = reRunScan(t, f, workerScanName, namespace)
				if err != nil {
					return err
				}

				// Scan has been re-started
				E2ELogf(t, "Scan phase should be reset")
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseRunning, compv1alpha1.ResultNotAvailable)
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				E2ELogf(t, "Let's wait for it to be done now")
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}
				E2ELogf(t, "scan re-run has finished")

				// Now the check should be passing
				checkNoDirectRootLogins := compv1alpha1.ComplianceCheckResult{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-no-direct-root-logins", workerScanName),
						Namespace: namespace,
					},
					ID:       "xccdf_org.ssgproject.content_rule_no_direct_root_logins",
					Status:   compv1alpha1.CheckResultPass,
					Severity: compv1alpha1.CheckResultSeverityMedium,
				}
				err = assertHasCheck(f, suiteName, workerScanName, checkNoDirectRootLogins)
				if err != nil {
					return err
				}

				// The test should not leave junk around, let's remove the MC and wait for the nodes to stabilize
				// again
				E2ELogf(t, "Removing applied remediation")
				// Fetch remediation here so it can be deleted
				rem := &compv1alpha1.ComplianceRemediation{}
				err = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: workersNoRootLoginsRemName, Namespace: namespace}, rem)
				if err != nil {
					return err
				}
				mcfgToBeDeleted := rem.Spec.Current.Object.DeepCopy()
				mcfgToBeDeleted.SetName(rem.GetMcName())
				err = f.Client.Delete(goctx.TODO(), mcfgToBeDeleted)
				if err != nil {
					return err
				}

				E2ELogf(t, "MC deleted, will wait for the machines to come back up")

				dummyAction := func() error {
					return nil
				}
				poolHasNoMc := func(t *testing.T, pool *mcfgv1.MachineConfigPool) (bool, error) {
					for _, mc := range pool.Status.Configuration.Source {
						if mc.Name == rem.GetMcName() {
							return false, nil
						}
					}

					return true, nil
				}

				// We need to wait for both the pool to update..
				err = waitForMachinePoolUpdate(t, f, testPoolName, dummyAction, poolHasNoMc, nil)
				if err != nil {
					t.Errorf("Failed to wait for workers to come back up after deleting MC")
					return err
				}

				// ..as well as the nodes
				err = waitForNodesToBeReady(t, f)
				if err != nil {
					t.Errorf("Failed to wait for nodes to come back up after applying MC: %v", err)
					return err
				}

				E2ELogf(t, "The test succeeded!")
				return nil
			},
		},
		testExecution{
			Name:       "TestUnapplyRemediation",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				// FIXME, maybe have a func that returns a struct with suite name and scan names?
				suiteName := "test-unapply-remediation"

				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)

				exampleComplianceSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									NodeSelector: getPoolNodeRoleSelector(),
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: workerScanName,
							},
						},
					},
				}

				err := mcTctx.createE2EPool()
				if err != nil {
					t.Errorf("Cannot create subpool for this test")
					return err
				}

				err = f.Client.Create(goctx.TODO(), exampleComplianceSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// Pause the MC so that we have only one reboot
				err = pauseMachinePool(t, f, testPoolName)
				if err != nil {
					return err
				}

				// Apply both remediations
				workersNoRootLoginsRemName := fmt.Sprintf("%s-no-direct-root-logins", workerScanName)
				err = applyRemediationAndCheck(t, f, namespace, workersNoRootLoginsRemName, testPoolName)
				if err != nil {
					E2ELogf(t, "WARNING: Got an error while applying remediation '%s': %v", workersNoRootLoginsRemName, err)
				}
				E2ELogf(t, "Remediation %s applied", workersNoRootLoginsRemName)

				workersNoEmptyPassRemName := fmt.Sprintf("%s-no-empty-passwords", workerScanName)
				err = applyRemediationAndCheck(t, f, namespace, workersNoEmptyPassRemName, testPoolName)
				if err != nil {
					E2ELogf(t, "WARNING: Got an error while applying remediation '%s': %v", workersNoEmptyPassRemName, err)
				}
				E2ELogf(t, "Remediation %s applied", workersNoEmptyPassRemName)

				// unpause the MCP so that the remediation gets applied
				err = unPauseMachinePoolAndWait(t, f, testPoolName)
				if err != nil {
					return err
				}

				err = waitForNodesToBeReady(t, f)
				if err != nil {
					t.Errorf("Failed to wait for nodes to come back up after applying MC: %v", err)
					return err
				}

				// Get the resulting MC
				mcName := types.NamespacedName{Name: fmt.Sprintf("75-%s-%s", workerScanName, suiteName)}
				mcBoth := &mcfgv1.MachineConfig{}
				err = f.Client.Get(goctx.TODO(), mcName, mcBoth)
				E2ELogf(t, "MC %s exists", mcName.Name)

				// Revert one remediation. The MC should stay, but its generation should bump
				E2ELogf(t, "Will revert remediation %s", workersNoEmptyPassRemName)
				err = unApplyRemediationAndCheck(t, f, namespace, workersNoEmptyPassRemName, testPoolName, false)
				if err != nil {
					E2ELogf(t, "WARNING: Got an error while unapplying remediation '%s': %v", workersNoEmptyPassRemName, err)
				}
				E2ELogf(t, "Remediation %s reverted", workersNoEmptyPassRemName)
				mcOne := &mcfgv1.MachineConfig{}
				err = f.Client.Get(goctx.TODO(), mcName, mcOne)

				if mcOne.Generation == mcBoth.Generation {
					t.Errorf("Expected that the MC generation changes. Got: %d, Expected: %d", mcOne.Generation, mcBoth.Generation)
				}

				// When we unapply the second remediation, the MC should be deleted, too
				E2ELogf(t, "Will revert remediation %s", workersNoRootLoginsRemName)
				err = unApplyRemediationAndCheck(t, f, namespace, workersNoRootLoginsRemName, testPoolName, true)
				E2ELogf(t, "Remediation %s reverted", workersNoEmptyPassRemName)

				E2ELogf(t, "No remediation-based MCs should exist now")
				mcShouldntExist := &mcfgv1.MachineConfig{}
				err = f.Client.Get(goctx.TODO(), mcName, mcShouldntExist)
				if err == nil {
					t.Errorf("MC %s unexpectedly found", mcName)
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestInconsistentResult",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				suiteName := "test-inconsistent"
				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}

				workersComplianceSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: "quay.io/complianceascode/ocp4:latest",
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_no_direct_root_logins",
									Content:      rhcosContentFile,
									NodeSelector: selectWorkers,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: workerScanName,
							},
						},
					},
				}

				workerNodes := getNodesWithSelector(f, selectWorkers)
				pod, err := createAndRemoveEtcSecurettyOnNode(t, f, namespace, "create-etc-securetty", workerNodes[0].Labels["kubernetes.io/hostname"])
				if err != nil {
					return err
				}

				err = f.Client.Create(goctx.TODO(), workersComplianceSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultInconsistent)
				if err != nil {
					t.Errorf("Got an unexpected status")
				}

				if err := f.KubeClient.CoreV1().Pods(namespace).Delete(goctx.TODO(), pod.Name, metav1.DeleteOptions{}); err != nil {
					return err
				}

				// The check for the no-direct-root-logins rule should be inconsistent
				var rootLoginCheck compv1alpha1.ComplianceCheckResult
				rootLoginCheckName := fmt.Sprintf("%s-no-direct-root-logins", workerScanName)

				err = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: rootLoginCheckName, Namespace: namespace}, &rootLoginCheck)
				if err != nil {
					return err
				}

				if rootLoginCheck.Status != compv1alpha1.CheckResultInconsistent {
					return fmt.Errorf("expected the %s result to be inconsistent, the check result was %s", rootLoginCheckName, rootLoginCheck.Status)
				}

				var expectedInconsistentSource string
				var shouldHaveMostCommonState bool

				if len(workerNodes) >= 3 {
					// The annotations should list the node that had a different result
					expectedInconsistentSource = workerNodes[0].Name + ":" + string(compv1alpha1.CheckResultPass)
					// Since all the other nodes consistently fail, there should also be a common result
					shouldHaveMostCommonState = true
				} else if len(workerNodes) == 2 {
					// example: ip-10-0-184-135.us-west-1.compute.internal:PASS,ip-10-0-226-48.us-west-1.compute.internal:FAIL
					expectedInconsistentSource = workerNodes[0].Name + ":" + string(compv1alpha1.CheckResultPass) + "," + workerNodes[1].Name + string(compv1alpha1.CheckResultFail)
					// If there are only two worker nodes, we won't be able to find the common status, so both
					// nodes would be listed as inconsistent -- we can't figure out which of the two results is
					// consistent and which is not
					shouldHaveMostCommonState = false
				} else {
					E2ELog(t, "Only one worker node? Shortcutting the test")
					return nil
				}

				inconsistentSources := rootLoginCheck.Annotations[compv1alpha1.ComplianceCheckResultInconsistentSourceAnnotation]
				if inconsistentSources != expectedInconsistentSource {
					return fmt.Errorf("expected that node %s would report %s, instead it reports %s", workerNodes[0].Name, expectedInconsistentSource, inconsistentSources)
				}

				if shouldHaveMostCommonState {
					mostCommonState := rootLoginCheck.Annotations[compv1alpha1.ComplianceCheckResultMostCommonAnnotation]
					if mostCommonState != string(compv1alpha1.CheckResultFail) {
						return fmt.Errorf("expected that there would be a common FAIL state, instead got %s", mostCommonState)
					}
				}

				// Since all states were either pass or fail, we still create the remediation
				workerRemediations := []string{
					fmt.Sprintf("%s-no-direct-root-logins", workerScanName),
				}
				err = assertHasRemediations(t, f, suiteName, workerScanName, "worker", workerRemediations)
				if err != nil {
					return err
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestPlatformAndNodeSuiteScan",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.TestCtx, mcTctx *mcTestCtx, namespace string) error {
				suiteName := "test-suite-two-scans-with-platform"

				workerScanName := fmt.Sprintf("%s-workers-scan", suiteName)
				selectWorkers := map[string]string{
					"node-role.kubernetes.io/worker": "",
				}

				platformScanName := fmt.Sprintf("%s-platform-scan", suiteName)

				exampleComplianceSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Content:      rhcosContentFile,
									NodeSelector: selectWorkers,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: workerScanName,
							},
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ScanType:     compv1alpha1.ScanTypePlatform,
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_ocp_idp_no_htpasswd",
									Content:      ocpContentFile,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: platformScanName,
							},
						},
					},
				}

				err := f.Client.Create(goctx.TODO(), exampleComplianceSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone,
					compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// At this point, both scans should be non-compliant given our current content
				err = scanResultIsExpected(t, f, namespace, workerScanName, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				// The profile should find one check for an htpasswd IDP, so we should be compliant.
				err = scanResultIsExpected(t, f, namespace, platformScanName, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}

				// Each scan should produce two remediations
				workerRemediations := []string{
					fmt.Sprintf("%s-no-empty-passwords", workerScanName),
					fmt.Sprintf("%s-no-direct-root-logins", workerScanName),
				}
				err = assertHasRemediations(t, f, suiteName, workerScanName, "worker", workerRemediations)
				if err != nil {
					return err
				}

				// TODO: Add check for future API remediation
				//platformRemediations := []string{
				//	fmt.Sprintf("%s-no-empty-passwords", platformScanName),
				//	fmt.Sprintf("%s-no-direct-root-logins", platformScanName),
				//}
				//err = assertHasRemediations(t, f, suiteName, platformScanName, "master", platformRemediations)
				//if err != nil {
				//	return err
				//}

				// Test a fail result from the platform scan. This fails the HTPasswd IDP check.
				if _, err := f.KubeClient.CoreV1().Secrets("openshift-config").Create(goctx.TODO(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "htpass",
						Namespace: "openshift-config",
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"htpasswd": []byte("bob:$2y$05$OyjQO7M2so4hRJW0aS9yie9KJ0wXv80XFWyEsApUZFURqE37aVR/a"),
					},
				}, metav1.CreateOptions{}); err != nil {
					return err
				}

				defer func() {
					err := f.KubeClient.CoreV1().Secrets("openshift-config").Delete(goctx.TODO(), "htpass", metav1.DeleteOptions{})
					if err != nil {
						t.Logf("could not clean up openshift-config/htpass test secret: %v", err)
					}
				}()

				fetchedOauth := &configv1.OAuth{}
				err = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: "cluster"}, fetchedOauth)
				if err != nil {
					return err
				}

				oauthUpdate := fetchedOauth.DeepCopy()
				oauthUpdate.Spec = configv1.OAuthSpec{
					IdentityProviders: []configv1.IdentityProvider{
						{
							Name:          "my_htpasswd_provider",
							MappingMethod: "claim",
							IdentityProviderConfig: configv1.IdentityProviderConfig{
								Type: "HTPasswd",
								HTPasswd: &configv1.HTPasswdIdentityProvider{
									FileData: configv1.SecretNameReference{
										Name: "htpass",
									},
								},
							},
						},
					},
				}

				err = f.Client.Update(goctx.TODO(), oauthUpdate)
				if err != nil {
					t.Logf("error updating idp: %v", err)
					return err
				}

				defer func() {
					fetchedOauth := &configv1.OAuth{}
					err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: "cluster"}, fetchedOauth)
					if err != nil {
						t.Logf("error restoring idp: %v", err)
					} else {
						oauth := fetchedOauth.DeepCopy()
						// Make sure it's cleared out
						oauth.Spec = configv1.OAuthSpec{
							IdentityProviders: nil,
						}
						err = f.Client.Update(goctx.TODO(), oauth)
						if err != nil {
							t.Logf("error restoring idp: %v", err)
						}
					}
				}()

				suiteName = "test-suite-two-scans-with-platform-2"
				platformScanName = fmt.Sprintf("%s-platform-scan-2", suiteName)
				exampleComplianceSuite = &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      suiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ScanType:     compv1alpha1.ScanTypePlatform,
									ContentImage: contentImagePath,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_ocp_idp_no_htpasswd",
									Content:      ocpContentFile,
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: platformScanName,
							},
						},
					},
				}

				err = f.Client.Create(goctx.TODO(), exampleComplianceSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, suiteName, compv1alpha1.PhaseDone,
					compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				err = scanResultIsExpected(t, f, namespace, platformScanName, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				return nil
			},
		},
		testExecution{
			Name:       "TestUpdateRemediation",
			IsParallel: false,
			TestFn: func(t *testing.T, f *framework.Framework, ctx *framework.Context, mcTctx *mcTestCtx, namespace string) error {
				origSuiteName := "test-update-remediation"
				workerScanName := fmt.Sprintf("%s-e2e-scan", origSuiteName)

				const (
					origImage = "quay.io/jhrozek/ocp4-openscap-content:rem_mod_base"
					modImage  = "quay.io/jhrozek/ocp4-openscap-content:rem_mod_change"
				)

				origSuite := &compv1alpha1.ComplianceSuite{
					ObjectMeta: metav1.ObjectMeta{
						Name:      origSuiteName,
						Namespace: namespace,
					},
					Spec: compv1alpha1.ComplianceSuiteSpec{
						ComplianceSuiteSettings: compv1alpha1.ComplianceSuiteSettings{
							AutoApplyRemediations: false,
						},
						Scans: []compv1alpha1.ComplianceScanSpecWrapper{
							{
								ComplianceScanSpec: compv1alpha1.ComplianceScanSpec{
									ContentImage: origImage,
									Profile:      "xccdf_org.ssgproject.content_profile_moderate",
									Rule:         "xccdf_org.ssgproject.content_rule_no_empty_passwords",
									Content:      rhcosContentFile,
									NodeSelector: getPoolNodeRoleSelector(),
									ComplianceScanSettings: compv1alpha1.ComplianceScanSettings{
										Debug: true,
									},
								},
								Name: workerScanName,
							},
						},
					},
				}

				err := mcTctx.createE2EPool()
				if err != nil {
					t.Errorf("Cannot create subpool for this test")
					return err
				}

				err = f.Client.Create(goctx.TODO(), origSuite, getCleanupOpts(ctx))
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, origSuiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultNonCompliant)
				if err != nil {
					return err
				}

				workersNoEmptyPassRemName := fmt.Sprintf("%s-no-empty-passwords", workerScanName)
				err = applyRemediationAndCheck(t, f, namespace, workersNoEmptyPassRemName, testPoolName)
				if err != nil {
					E2ELogf(t, "WARNING: Got an error while applying remediation '%s': %v", workersNoEmptyPassRemName, err)
				}
				E2ELogf(t, "Remediation %s applied", workersNoEmptyPassRemName)

				err = waitForNodesToBeReady(t, f)
				if err != nil {
					t.Errorf("Failed to wait for nodes to come back up after applying MC: %v", err)
					return err
				}

				// Now update the suite with a different image that contains different remediations
				err = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: origSuiteName, Namespace: namespace}, origSuite)
				if err != nil {
					return err
				}
				modSuite := origSuite.DeepCopy()
				modSuite.Spec.Scans[0].ContentImage = modImage
				err = f.Client.Update(goctx.TODO(), modSuite)
				if err != nil {
					return err
				}
				E2ELogf(t, "Suite %s updated with a new image", modSuite.Name)

				err = reRunScan(t, f, workerScanName, namespace)
				if err != nil {
					return err
				}

				// Ensure that all the scans in the suite have finished and are marked as Done
				err = waitForSuiteScansStatus(t, f, namespace, origSuiteName, compv1alpha1.PhaseDone, compv1alpha1.ResultCompliant)
				if err != nil {
					return err
				}

				err, isObsolete := remediationIsObsolete(t, f, namespace, workersNoEmptyPassRemName)
				if err != nil {
					return err
				}
				if isObsolete == false {
					return fmt.Errorf("expected that the remediation is obsolete")
				}

				E2ELog(t, "Will remove obsolete data from remediation")
				renderedMcName := fmt.Sprintf("75-%s-%s", workerScanName, origSuiteName)
				err = removeObsoleteRemediationAndCheck(t, f, namespace, workersNoEmptyPassRemName, renderedMcName, testPoolName)
				if err != nil {
					return err
				}

				err = waitForNodesToBeReady(t, f)
				if err != nil {
					t.Errorf("Failed to wait for nodes to come back up after applying MC: %v", err)
					return err
				}

				// Now the remediation is no longer obsolete
				err, isObsolete = remediationIsObsolete(t, f, namespace, workersNoEmptyPassRemName)
				if err != nil {
					return err
				}

				if isObsolete == true {
					return fmt.Errorf("expected that the remediation is no longer obsolete")
				}

				// Finally clean up by removing the remediation and waiting for the nodes to reboot one more time
				err = unApplyRemediationAndCheck(t, f, namespace, workersNoEmptyPassRemName, testPoolName, true)
				if err != nil {
					return err
				}

				err = waitForNodesToBeReady(t, f)
				if err != nil {
					t.Errorf("Failed to wait for nodes to come back up after unapplying MC: %v", err)
					return err
				}

				return nil
			},
		},
	)
}
