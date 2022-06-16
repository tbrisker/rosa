package aws

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/iam"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
)

// PolicyDocument models an AWS IAM policy document
type PolicyDocument struct {
	ID string `json:"Id,omitempty"`
	// Specify the version of the policy language that you want to use.
	// As a best practice, use the latest 2012-10-17 version.
	Version string `json:"Version,omitempty"`
	// Use this main policy element as a container for the following elements.
	// You can include more than one statement in a policy.
	Statement []PolicyStatement `json:"Statement"`
}

// PolicyStatement models an AWS policy statement entry.
type PolicyStatement struct {
	// Include an optional statement ID to differentiate between your statements.
	Sid string `json:"Sid,omitempty"`
	// Use `Allow` or `Deny` to indicate whether the policy allows or denies access.
	Effect string `json:"Effect"`
	// If you create a resource-based policy, you must indicate the account, user, role, or
	// federated user to which you would like to allow or deny access. If you are creating an
	// IAM permissions policy to attach to a user or role, you cannot include this element.
	// The principal is implied as that user or role.
	Principal *PolicyStatementPrincipal `json:"Principal,omitempty"`
	// Include a list of actions that the policy allows or denies.
	// (i.e. ec2:StartInstances, iam:ChangePassword)
	Action interface{} `json:"Action,omitempty"`
	// If you create an IAM permissions policy, you must specify a list of resources to which
	// the actions apply. If you create a resource-based policy, this element is optional. If
	// you do not include this element, then the resource to which the action applies is the
	// resource to which the policy is attached.
	Resource interface{} `json:"Resource,omitempty"`
}

type PolicyStatementPrincipal struct {
	// A service principal is an identifier that is used to grant permissions to a service.
	// The identifier for a service principal includes the service name, and is usually in the
	// following format: service-name.amazonaws.com
	Service []string `json:"Service,omitempty"`
	// You can specify an individual IAM role ARN (or array of role ARNs) as the principal.
	// In IAM roles, the Principal element in the role's trust policy specifies who can assume the role.
	// When you specify more than one principal in the element, you grant permissions to each principal.
	AWS interface{} `json:"AWS,omitempty"`
	// A federated principal uses a web identity token or SAML federation
	Federated string `json:"Federated,omitempty"`
}

func NewPolicyDocument() *PolicyDocument {
	return &PolicyDocument{Version: "2012-10-17"}
}

func ParsePolicyDocument(doc string) (PolicyDocument, error) {
	policy := PolicyDocument{}
	err := json.Unmarshal([]byte(doc), &policy)
	return policy, err
}

func (p *PolicyStatement) GetAWSPrincipals() []string {
	awsPrincipal := p.Principal.AWS
	var awsArr []string
	if awsPrincipal == nil {
		return awsArr
	}
	switch reflect.TypeOf(awsPrincipal).Kind() {
	case reflect.Slice:
		value := reflect.ValueOf(awsPrincipal)
		awsArr = make([]string, value.Len())
		for i := 0; i < value.Len(); i++ {
			awsArr[i] = value.Index(i).Interface().(string)
		}
	case reflect.String:
		awsArr = make([]string, 1)
		awsArr[0] = awsPrincipal.(string)
	}
	return awsArr
}

// AllowActions adds a statement to a policy allowing the provided actions for all Resources.
// If you need a more compilex statement it is better to construct it manually.
func (p *PolicyDocument) AllowActions(actions ...string) {
	statement := PolicyStatement{Effect: "Allow", Action: actions, Resource: "*"}
	p.Statement = append(p.Statement, statement)
}

// IsActionAllowed checks if any of the statements in the document allows the wanted action.
// It does not take into account Resource or Principal constraints on the action.
func (p *PolicyDocument) IsActionAllowed(wanted string) bool {
	statements := p.Statement
	if len(statements) == 0 {
		return false
	}
	for _, statement := range statements {
		if statement.Effect != "Allow" {
			continue
		}
		switch action := statement.Action.(type) {
		case string:
			if action == wanted {
				return true
			}
		case []interface{}:
			for _, el := range action {
				if a, ok := el.(string); ok && a == wanted {
					return true
				}
			}
		}
	}
	return false
}

func (p PolicyDocument) String() string {
	res, err := json.Marshal(p)
	if err != nil {
		return fmt.Sprintf("Error marshalling policy document: %v", err)
	}
	return string(res)
}

func updateAssumeRolePolicyPrincipals(policy string, role *iam.Role) (string, bool, error) {
	oldPolicy, err := url.QueryUnescape(aws.StringValue(role.AssumeRolePolicyDocument))
	if err != nil {
		return policy, false, err
	}

	newPolicyDoc, err := ParsePolicyDocument(policy)
	if err != nil {
		return policy, false, err
	}

	// Determine if role already contains trusted principal
	principals := []string{}
	hasMultiplePrincipals := false
	for _, statement := range newPolicyDoc.Statement {
		awsPrincipals := statement.GetAWSPrincipals()
		// There is no AWS principal to add, nothing to do here
		if len(awsPrincipals) == 0 {
			return policy, false, nil
		}
		for _, trust := range awsPrincipals {
			// Trusted principal already exists, nothing to do here
			if strings.Contains(oldPolicy, trust) {
				return policy, false, nil
			}
			if strings.Contains(oldPolicy, `"AWS":[`) {
				hasMultiplePrincipals = true
			}
			principals = append(principals, trust)
		}
	}
	oldPrincipals := strings.Join(principals, `","`)

	// Extract existing trusted principals from existing role trust policy.
	// The AWS API is ambiguous faced with 1 vs many entries, so we cannot
	// unmarshal and have to resort to string matching...
	startSearch := `"AWS":"`
	endSearch := `"`
	if hasMultiplePrincipals {
		startSearch = `"AWS":["`
		endSearch = `"]`
	}
	start := strings.Index(oldPolicy, startSearch)
	if start >= 0 {
		start += len(startSearch)
		end := start + strings.Index(oldPolicy[start:], endSearch)
		if end >= start {
			principals = append(principals, strings.Split(oldPolicy[start:end], `","`)...)
		}
	}

	// Update assume role policy document to contain all trusted principals
	policy = strings.Replace(policy, oldPrincipals, strings.Join(principals, `","`), 1)

	return policy, true, nil
}

func InterpolatePolicyDocument(doc string, replacements map[string]string) string {
	for key, val := range replacements {
		doc = strings.Replace(doc, fmt.Sprintf("%%{%s}", key), val, -1)
	}
	return doc
}

func getPolicyDocument(policyDocument *string) (PolicyDocument, error) {
	data := PolicyDocument{}
	if policyDocument != nil {
		val, err := url.QueryUnescape(aws.StringValue(policyDocument))
		if err != nil {
			return data, err
		}
		return ParsePolicyDocument(val)
	}
	return data, nil
}

func GenerateRolePolicyDoc(cluster *cmv1.Cluster, accountID string, operator *cmv1.STSOperator,
	policyDetails string) (string, error) {
	oidcEndpointURL, err := url.ParseRequestURI(cluster.AWS().STS().OIDCEndpointURL())
	if err != nil {
		return "", err
	}
	issuerURL := fmt.Sprintf("%s%s", oidcEndpointURL.Host, oidcEndpointURL.Path)

	oidcProviderARN := fmt.Sprintf("arn:aws:iam::%s:oidc-provider/%s", accountID, issuerURL)

	serviceAccounts := []string{}
	for _, sa := range operator.ServiceAccounts() {
		serviceAccounts = append(serviceAccounts,
			fmt.Sprintf("system:serviceaccount:%s:%s", operator.Namespace(), sa))
	}

	policy := InterpolatePolicyDocument(policyDetails, map[string]string{
		"oidc_provider_arn": oidcProviderARN,
		"issuer_url":        issuerURL,
		"service_accounts":  strings.Join(serviceAccounts, `" , "`),
	})

	return policy, nil
}