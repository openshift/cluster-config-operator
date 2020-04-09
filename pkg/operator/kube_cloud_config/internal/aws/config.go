package aws

// Copied from https://github.com/kubernetes/kubernetes/blob/9e991415386e4cf155a24b1da15becaa390438d8/staging/src/k8s.io/legacy-cloud-providers/aws/aws.go#L541-L616
// to avoid dependency on `k8s.io/legacy-cloud-providers`

// CloudConfig wraps the settings for the AWS cloud provider.
// NOTE: Cloud config files should follow the same Kubernetes deprecation policy as
// flags or CLIs. Config fields should not change behavior in incompatible ways and
// should be deprecated for at least 2 release prior to removing.
// See https://kubernetes.io/docs/reference/using-api/deprecation-policy/#deprecating-a-flag-or-cli
// for more details.
type CloudConfig struct {
	Global struct {
		// TODO: Is there any use for this?  We can get it from the instance metadata service
		// Maybe if we're not running on AWS, e.g. bootstrap; for now it is not very useful
		Zone string

		// The AWS VPC flag enables the possibility to run the master components
		// on a different aws account, on a different cloud provider or on-premises.
		// If the flag is set also the KubernetesClusterTag must be provided
		VPC string
		// SubnetID enables using a specific subnet to use for ELB's
		SubnetID string
		// RouteTableID enables using a specific RouteTable
		RouteTableID string

		// RoleARN is the IAM role to assume when interaction with AWS APIs.
		RoleARN string

		// KubernetesClusterTag is the legacy cluster id we'll use to identify our cluster resources
		KubernetesClusterTag string
		// KubernetesClusterID is the cluster id we'll use to identify our cluster resources
		KubernetesClusterID string

		//The aws provider creates an inbound rule per load balancer on the node security
		//group. However, this can run into the AWS security group rule limit of 50 if
		//many LoadBalancers are created.
		//
		//This flag disables the automatic ingress creation. It requires that the user
		//has setup a rule that allows inbound traffic on kubelet ports from the
		//local VPC subnet (so load balancers can access it). E.g. 10.82.0.0/16 30000-32000.
		DisableSecurityGroupIngress bool

		//AWS has a hard limit of 500 security groups. For large clusters creating a security group for each ELB
		//can cause the max number of security groups to be reached. If this is set instead of creating a new
		//Security group for each ELB this security group will be used instead.
		ElbSecurityGroup string

		//During the instantiation of an new AWS cloud provider, the detected region
		//is validated against a known set of regions.
		//
		//In a non-standard, AWS like environment (e.g. Eucalyptus), this check may
		//be undesirable.  Setting this to true will disable the check and provide
		//a warning that the check was skipped.  Please note that this is an
		//experimental feature and work-in-progress for the moment.  If you find
		//yourself in an non-AWS cloud and open an issue, please indicate that in the
		//issue body.
		DisableStrictZoneCheck bool
	}
	// [ServiceOverride "1"]
	//  Service = s3
	//  Region = region1
	//  URL = https://s3.foo.bar
	//  SigningRegion = signing_region
	//  SigningMethod = signing_method
	//
	//  [ServiceOverride "2"]
	//     Service = ec2
	//     Region = region2
	//     URL = https://ec2.foo.bar
	//     SigningRegion = signing_region
	//     SigningMethod = signing_method
	ServiceOverride map[string]*struct {
		Service       string
		Region        string
		URL           string
		SigningRegion string
		SigningMethod string
		SigningName   string
	}
}
