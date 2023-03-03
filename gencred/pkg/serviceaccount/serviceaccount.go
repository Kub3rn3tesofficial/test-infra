/*
Copyright 2019 The Kubernetes Authors.

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

package serviceaccount

import (
	"context"
	"errors"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/test-infra/gencred/pkg/kubeconfig"
)

const (
	// clusterRole is the ClusterRole role reference for created ClusterRoleBinding.
	clusterRole = "cluster-admin"
	// clusterRoleBindingName is the name for the created ClusterRoleBinding.
	clusterRoleBindingName = "serviceaccount-cluster-admin-crb"
	// serviceAccountName is the name for the created ServiceAccount.
	serviceAccountName = "serviceaccount-cluster-admin"
)

// checkSAAuth checks authorization for required cluster service account (SA) resources.
func checkSAAuth(clientset kubernetes.Interface) error {
	client := clientset.AuthorizationV1().SelfSubjectAccessReviews()

	// https://kubernetes.io/docs/reference/access-authn-authz/rbac/#privilege-escalation-prevention-and-bootstrapping
	if sar, err := client.Create(
		context.TODO(),
		&authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Group:    "rbac.authorization.k8s.io",
					Verb:     "bind",
					Resource: "clusterroles",
					Name:     clusterRole,
				},
			},
		},
		metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("bind %s: %w", clusterRole, err)
	} else if !sar.Status.Allowed {
		return fmt.Errorf("not authorized to bind %s: %s", clusterRole, sar.Status.Reason)
	}

	return nil
}

// getOrCreateSA gets existing or creates new service account (SA).
func getOrCreateSA(clientset kubernetes.Interface, duration metav1.Duration) ([]byte, []byte, error) {
	client := clientset.CoreV1().ServiceAccounts(corev1.NamespaceDefault)

	// Check SelfSubjectAccessReviews are allowed.
	if err := checkSAAuth(clientset); err != nil {
		return nil, nil, err
	}

	// Create ServiceAccount if not exists.
	if _, err := client.Get(context.TODO(), serviceAccountName, metav1.GetOptions{}); err != nil {
		// Generate a Kubernetes ServiceAccount object.
		saObj := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: serviceAccountName,
			},
		}

		// Create ServiceAccount.
		_, err := client.Create(context.TODO(), saObj, metav1.CreateOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("create SA: %w", err)
		}
	}

	// Get/Create ClusterRoleBinding.
	err := getOrCreateCRB(clientset)
	if err != nil {
		return nil, nil, fmt.Errorf("get or create CRB: %w", err)
	}

	// Get ServiceAccount.
	saObj, err := client.Get(context.TODO(), serviceAccountName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("get SA: %w", err)
	}

	return getSASecrets(clientset, saObj, duration)
}

// getOrCreateCRB gets existing or creates new cluster role binding (CRB).
func getOrCreateCRB(clientset kubernetes.Interface) error {
	client := clientset.RbacV1().ClusterRoleBindings()

	// Get ClusterRoleBinding if exists.
	if _, err := client.Get(context.TODO(), clusterRoleBindingName, metav1.GetOptions{}); err == nil {
		return nil
	}

	// Generate a Kubernetes ClusterRoleBinding object.
	crbObj := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterRoleBindingName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccountName, Namespace: corev1.NamespaceDefault}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: clusterRole},
	}

	// Create ClusterRoleBinding.
	_, err := client.Create(context.TODO(), crbObj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create CRB: %w", err)
	}

	return nil
}

// getSASecrets gets service account token and root CA secrets.
func getSASecrets(clientset kubernetes.Interface, saObj *corev1.ServiceAccount, duration metav1.Duration) ([]byte, []byte, error) {
	durationInSeconds := int64(duration.Seconds())
	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &durationInSeconds,
		},
	}
	tokenResp, err := clientset.CoreV1().ServiceAccounts(saObj.Namespace).CreateToken(context.TODO(), saObj.Name, tokenReq, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("creating service account token: %w", err)
	}
	if tokenResp.Status.Token == "" {
		return nil, nil, fmt.Errorf("no service account token returned")
	}

	caConfigMap, err := clientset.CoreV1().ConfigMaps(corev1.NamespaceDefault).Get(context.TODO(), "kube-root-ca.crt", metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("locate root CA configmap: %w", err)
	}
	caPEM, ok := caConfigMap.Data["ca.crt"]
	if !ok {
		return nil, nil, errors.New("locate root CA data")
	}

	return []byte(tokenResp.Status.Token), []byte(caPEM), nil
}

// CreateClusterServiceAccountCredentials creates a service account to authenticate to a cluster API server.
func CreateClusterServiceAccountCredentials(clientset kubernetes.Interface, duration metav1.Duration) (token []byte, caPEM []byte, err error) {
	token, caPEM, err = getOrCreateSA(clientset, duration)
	if err != nil {
		return nil, nil, fmt.Errorf("get or create SA: %w", err)
	}

	return token, caPEM, nil
}

// CreateKubeConfigWithServiceAccountCredentials creates a kube config containing a service account token to authenticate to a Kubernetes cluster API server.
func CreateKubeConfigWithServiceAccountCredentials(clientset kubernetes.Interface, name string, duration metav1.Duration) ([]byte, error) {
	token, caPEM, err := CreateClusterServiceAccountCredentials(clientset, duration)
	if err != nil {
		return nil, err
	}

	authInfo := clientcmdapi.AuthInfo{
		Token: string(token),
	}

	return kubeconfig.CreateKubeConfig(clientset, name, caPEM, authInfo)
}
