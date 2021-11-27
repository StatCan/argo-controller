# argo-controller

A series of controllers for configuring namespaces to accomodate Argo.

## ArgoCD

- TBD

## Argo Workflows

- Make a service account in every namespace with the active directory group annotation
- Create a role binding in every namespace mapping to a cluster role granting access to argo workflow resources
- Create a secret for accessing storage through MinIO in gateway mode
