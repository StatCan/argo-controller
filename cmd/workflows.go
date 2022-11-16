package cmd

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/StatCan/argo-controller/pkg/controllers/namespaces"
	"github.com/StatCan/argo-controller/pkg/signals"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

var workflowsCmd = &cobra.Command{
	Use:   "workflows",
	Short: "Configure access control resources for Argo Workflows",
	Long:  `Configure access control resources for Argo Workflows.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Setup signals so we can shutdown cleanly
		stopCh := signals.SetupSignalHandler()

		// Create Kubernetes config
		cfg, err := clientcmd.BuildConfigFromFlags(apiserver, kubeconfig)
		if err != nil {
			klog.Fatalf("error building kubeconfig: %v", err)
		}

		kubeClient, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
		}

		// Setup informers
		kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, time.Minute*5)

		// Namespaces informer
		namespaceInformer := kubeInformerFactory.Core().V1().Namespaces()
		// namespaceLister := namespaceInformer.Lister()

		// Serviceaccount informer
		serviceAccountsInformer := kubeInformerFactory.Core().V1().ServiceAccounts()
		serviceAccountsLister := serviceAccountsInformer.Lister()

		// Rolebinding informer
		roleBindingInformer := kubeInformerFactory.Rbac().V1().RoleBindings()
		roleBindingLister := roleBindingInformer.Lister()

		// Secrets informer
		secretsInformer := kubeInformerFactory.Core().V1().Secrets()
		secretsLister := secretsInformer.Lister()

		// Setup controller
		controller := namespaces.NewController(
			namespaceInformer,
			func(namespace *corev1.Namespace) error {
				// Generate SA
				serviceAccounts, err := generateServiceAccounts(namespace, roleBindingLister)
				if err != nil {
					return err
				}

				// Generate RBAC
				roleBindings, err := generateRoleBindings(namespace, roleBindingLister)

				// Generate Secrets
				secrets := generateSecrets(namespace)

				// Create
				for _, serviceAccount := range serviceAccounts {
					currentServiceAccount, err := serviceAccountsLister.ServiceAccounts(serviceAccount.Namespace).Get(serviceAccount.Name)
					if errors.IsNotFound(err) {
						klog.Infof("creating service account %s/%s", serviceAccount.Namespace, serviceAccount.Name)
						currentServiceAccount, err = kubeClient.CoreV1().ServiceAccounts(serviceAccount.Namespace).Create(context.Background(), serviceAccount, metav1.CreateOptions{})
						if err != nil {
							return err
						}
					}

					if !reflect.DeepEqual(serviceAccount.Annotations, currentServiceAccount.Annotations) {
						klog.Infof("updating service account %s/%s", serviceAccount.Namespace, serviceAccount.Name)
						currentServiceAccount.Annotations = serviceAccount.Annotations

						_, err = kubeClient.CoreV1().ServiceAccounts(serviceAccount.Namespace).Update(context.Background(), currentServiceAccount, metav1.UpdateOptions{})
						if err != nil {
							return err
						}
					}
				}

				for _, roleBinding := range roleBindings {
					currentRoleBinding, err := roleBindingLister.RoleBindings(roleBinding.Namespace).Get(roleBinding.Name)
					if errors.IsNotFound(err) {
						klog.Infof("creating role binding %s/%s", roleBinding.Namespace, roleBinding.Name)
						currentRoleBinding, err = kubeClient.RbacV1().RoleBindings(roleBinding.Namespace).Create(context.Background(), roleBinding, metav1.CreateOptions{})
						if err != nil {
							return err
						}
					}

					if !reflect.DeepEqual(roleBinding.RoleRef, currentRoleBinding.RoleRef) || !reflect.DeepEqual(roleBinding.Subjects, currentRoleBinding.Subjects) {
						klog.Infof("updating role binding %s/%s", roleBinding.Namespace, roleBinding.Name)
						currentRoleBinding.RoleRef = roleBinding.RoleRef
						currentRoleBinding.Subjects = roleBinding.Subjects

						_, err = kubeClient.RbacV1().RoleBindings(roleBinding.Namespace).Update(context.Background(), currentRoleBinding, metav1.UpdateOptions{})
						if err != nil {
							return err
						}
					}
				}

				for _, secret := range secrets {
					currentSecret, err := secretsLister.Secrets(secret.Namespace).Get(secret.Name)
					if errors.IsNotFound(err) {
						klog.Infof("creating secret %s/%s", secret.Namespace, secret.Name)
						currentSecret, err = kubeClient.CoreV1().Secrets(secret.Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
						if err != nil {
							return err
						}
					}

					if !reflect.DeepEqual(secret.Data, currentSecret.Data) {
						klog.Infof("updating secret %s/%s", secret.Namespace, secret.Name)
						currentSecret.Data = secret.Data

						_, err = kubeClient.CoreV1().Secrets(secret.Namespace).Update(context.Background(), currentSecret, metav1.UpdateOptions{})
						if err != nil {
							return err
						}
					}
				}

				return nil
			},
		)

		serviceAccountsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(old, new interface{}) {
				newNP := new.(*corev1.ServiceAccount)
				oldNP := old.(*corev1.ServiceAccount)

				if newNP.ResourceVersion == oldNP.ResourceVersion {
					return
				}

				controller.HandleObject(new)
			},
			DeleteFunc: controller.HandleObject,
		})

		roleBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(old, new interface{}) {
				newNP := new.(*rbacv1.RoleBinding)
				oldNP := old.(*rbacv1.RoleBinding)

				if newNP.ResourceVersion == oldNP.ResourceVersion {
					return
				}

				controller.HandleObject(new)
			},
			DeleteFunc: controller.HandleObject,
		})

		secretsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(old, new interface{}) {
				newNP := new.(*corev1.Secret)
				oldNP := old.(*corev1.Secret)

				if newNP.ResourceVersion == oldNP.ResourceVersion {
					return
				}

				controller.HandleObject(new)
			},
			DeleteFunc: controller.HandleObject,
		})

		// Start informers
		kubeInformerFactory.Start(stopCh)

		// Wait for caches
		klog.Info("Waiting for informer caches to sync")
		if ok := cache.WaitForCacheSync(stopCh, serviceAccountsInformer.Informer().HasSynced, roleBindingInformer.Informer().HasSynced, secretsInformer.Informer().HasSynced); !ok {
			klog.Fatalf("failed to wait for caches to sync")
		}

		// Run the controller
		if err = controller.Run(2, stopCh); err != nil {
			klog.Fatalf("error running controller: %v", err)
		}
	},
}

// generateServiceAccounts generates service accounts for argo workflows.
func generateServiceAccounts(namespace *corev1.Namespace, roleBindingLister rbacv1listers.RoleBindingLister) ([]*corev1.ServiceAccount, error) {
	serviceAccounts := []*corev1.ServiceAccount{}

	// Find groups in the namespace admins
	roleBinding, err := roleBindingLister.RoleBindings(namespace.Name).Get("namespace-admins")
	if err != nil {
		if errors.IsNotFound(err) {
			return []*corev1.ServiceAccount{}, nil
		}

		return nil, err
	}


	// The default service account
	serviceAccounts = append(serviceAccounts, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-default-login",
			Namespace: namespace.Name,
			Annotations: map[string]string{
				"workflows.argoproj.io/rbac-rule": "true",
				"workflows.argoproj.io/rbac-rule-precedence": "0",
			},
		},
	})

	// Service account for argo-workflows itself
	serviceAccounts = append(serviceAccounts, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argo-workflows",
			Namespace: namespace.Name,
		},
	})

	// Service account for UI access
	for i, subject := range roleBinding.Subjects {
		if subject.Kind == "Group" {
			serviceAccounts = append(serviceAccounts, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:        fmt.Sprintf("argo-workflows-%v", subject.Name),
					Namespace:   namespace.Name,
					Annotations: map[string]string{
						"workflows.argoproj.io/rbac-rule": fmt.Sprintf("'%s' in groups", subject.Name),
						"workflows.argoproj.io/rbac-rule-precedence": fmt.Sprintf("%d", i),
					},
				},
			})
		}
	}

	return serviceAccounts, nil
}

// generateRoleBindings generates role bindings for argo workflows.
func generateRoleBindings(namespace *corev1.Namespace, roleBindingLister rbacv1listers.RoleBindingLister) ([]*rbacv1.RoleBinding, error) {
	roleBindings := []*rbacv1.RoleBinding{}

	// Find groups in the namespace admins
	roleBinding, err := roleBindingLister.RoleBindings(namespace.Name).Get("namespace-admins")
	if err != nil {
		if errors.IsNotFound(err) {
			return []*rbacv1.RoleBinding{}, nil
		}

		return nil, err
	}

	for _, subject := range roleBinding.Subjects {
		if subject.Kind == "Group" {
			roleBindings = append(roleBindings, &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("argo-workflows-%v", namespace.Name),
					Namespace: namespace.Name,
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.SchemeGroupVersion.Group,
					Kind:     "ClusterRole",
					Name:     "argo-workflows-namespace",
				},
				Subjects: []rbacv1.Subject{
					{
						APIGroup:  "",
						Kind:      "ServiceAccount",
						Name:      fmt.Sprintf("argo-workflows-%v", subject.Name),
						Namespace: subject.Name,
					},
				},
			})
		}
	}

	roleBindings = append(roleBindings, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argo-workflows",
			Namespace: namespace.Name,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     "argo-workflows-workflow",
		},
		Subjects: []rbacv1.Subject{
			{
				APIGroup:  "",
				Kind:      "ServiceAccount",
				Name:      "argo-workflows",
				Namespace: namespace.Name,
			},
		},
	})

	return roleBindings, nil
}

// generateSecrets generates secrets for argo workflows.
func generateSecrets(namespace *corev1.Namespace) []*corev1.Secret {
	secrets := []*corev1.Secret{}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "core/v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      os.Getenv("ARGO_SECRET_NAME"),
			Namespace: namespace.Name,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"root-user":     []byte(os.Getenv("ARGO_STORAGE_ACCOUNT_NAME")),
			"root-password": []byte(os.Getenv("ARGO_STORAGE_ACCOUNT_KEY")),
		},
	}

	secrets = append(secrets, secret)

	return secrets
}

func init() {
	rootCmd.AddCommand(workflowsCmd)
}
