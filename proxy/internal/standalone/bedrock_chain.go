package standalone

import (
	"context"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// bedrockSigningCredential resolves the IAM principal the proxy signs Bedrock
// requests with. The configured env pair keeps precedence (unchanged contract:
// AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY). Only when the environment says
// nothing does the AWS default chain run — ECS task role, EKS pod identity,
// IRSA, EC2 instance profile — so a proxy deployed inside a VPC needs no pasted
// keys. A PARTIAL env pair still fails closed inside the chain (awscreds.fromEnv
// errors rather than skipping): a typo'd secret variable must not silently
// switch the signing principal, and therefore the bill and the CloudTrail
// identity, to whatever ambient role the host carries. A chain miss returns
// the same empty credential the env path always returned; the Bedrock adapter
// turns that into its existing actionable error, so no new failure shape is
// introduced here.
func (c Creds) bedrockSigningCredential(ctx context.Context) providers.Credential {
	credential := c.cfg.BedrockSigningCredential()
	if credential.Key != "" || c.bedrock == nil {
		return credential
	}
	creds, err := c.bedrock.Credentials(ctx)
	if err != nil || !creds.Valid() {
		return credential
	}
	if c.logger != nil && c.sourceLogged != nil {
		c.sourceLogged.Do(func() {
			c.logger.Info("bedrock signing credentials resolved from the AWS default chain", "source", c.bedrock.Source())
		})
	}
	// Same colon encoding the adapter already parses for env and
	// x-cave-upstream-key credentials (bedrock.parseAWSCredentials).
	credential.Key = creds.AccessKeyID + ":" + creds.SecretAccessKey
	if creds.SessionToken != "" {
		credential.Key += ":" + creds.SessionToken
	}
	credential.AuthKind = "aws_access_keys"
	return credential
}
