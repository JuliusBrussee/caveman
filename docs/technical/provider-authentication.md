# Provider authentication through the local proxy

The standalone proxy forwards the credential selected by the caller. Its
environment supplies a fallback when the caller does not send a credential.
This matters for a persistent listener: an API key in the proxy's environment
must not replace the key chosen by a different client.

| Provider | Caller authentication | Environment fallback |
| --- | --- | --- |
| Anthropic | `x-api-key` or `Authorization: Bearer` | `ANTHROPIC_API_KEY`, sent as `x-api-key` |
| OpenAI | `Authorization: Bearer` | `OPENAI_API_KEY` |
| Gemini Developer API | `x-goog-api-key`, `key` or `$key` query parameter; Bearer requests also supply `x-goog-user-project` | `GEMINI_API_KEY`, then `GOOGLE_API_KEY` |
| Azure OpenAI | `api-key` or `Authorization: Bearer` | `AZURE_OPENAI_API_KEY`, sent as `api-key` |
| Bedrock | Bearer API key or an SDK SigV4 request re-signed as described below | `AWS_BEARER_TOKEN_BEDROCK`, otherwise the AWS access-key environment variables |
| Vertex AI | `Authorization: Bearer` with a current Google access token, or Express-mode `x-goog-api-key` / `key` / `$key` | None; the caller supplies the credential |
| Named OpenAI-compatible upstream | Bearer, or `x-api-key` for supported Anthropic Messages mounts | The exact mount's `api_key_env` |

Gemini, Vertex, and Azure select their native API-key header before the legacy
`x-api-key` alias. That alias remains supported on other provider routes.
Explicit API-key headers take precedence over `Authorization` when both are
present. A native `api-key` header is considered only for Azure, and a native
`x-goog-api-key` header only for Gemini and Vertex. Neither can supply an
unrelated provider's credential.

Gemini URL keys are decoded into `x-goog-api-key` and removed from the upstream
URL. Other query parameters retain their original bytes and order. Different
keys in the native header or repeated `key`/`$key` parameters return HTTP 400
with code `cave_provider_credentials_conflict`, without sending either key to
Google or adding an environment key. When a caller explicitly supplies an
OAuth bearer and a URL key, both caller inputs are retained; no environment key
is added. This normalization follows Google's equivalent
[API-key system parameters](https://docs.cloud.google.com/apis/docs/system-parameters)
and [recommended API-key header](https://docs.cloud.google.com/docs/authentication/api-keys-use).

Vertex Express uses the same Google API-key normalization, including conflict
rejection and preservation of other query bytes. It does not borrow a key from
the Gemini environment. An Express key combined with an explicit OAuth bearer
returns HTTP 400; the exact synthetic `Bearer no-key-required` marker can be
displaced by a supplied API key. This matches the Google SDK's separate API-key
and OAuth credential modes. Existing Vertex OAuth, quota-project and traffic-type
headers remain intact. See Google's
[Express-mode sample](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/samples/googlegenaisdk-vertexai-express-mode)
and [SDK credential selection](https://github.com/googleapis/python-genai/blob/main/google/genai/_api_client.py).

Azure preserves an explicitly supplied Bearer scheme for Microsoft Entra
tokens and the OpenAI v1 SDK's Bearer API-key flow. Key bytes are opaque: a key
starting with `eyJ` is not automatically relabelled as a token. The exact
synthetic `Bearer no-key-required` marker remains a request for the configured
fallback; it is not sent to Azure. These mappings follow
[Microsoft's authentication reference](https://learn.microsoft.com/en-us/azure/foundry/openai/latest),
[the OpenAI Azure SDK](https://github.com/openai/openai-python/blob/main/src/openai/lib/azure.py),
and [the OpenAI client's Bearer header](https://github.com/openai/openai-python/blob/main/src/openai/_client.py).
Gemini's native header follows
[Google's SDK](https://github.com/googleapis/python-genai/blob/main/google/genai/_api_client.py).

## AWS SDK signing

An AWS SigV4 signature covers the request's authority, path, headers and body.
Changing the base URL or adding a cache marker invalidates that signature.
The proxy therefore creates a new signature for the final upstream request;
it never puts an incoming `AWS4-HMAC-SHA256` header inside a Bearer token.
See [AWS signing documentation](https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html)
and [the AWS SDK's signing code](https://github.com/boto/botocore/blob/develop/botocore/auth.py).

For an SDK request signed with IAM credentials, the proxy process needs matching
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Temporary credentials also need
the matching `AWS_SESSION_TOKEN`. The configured Bedrock region must match the
request's signing region. These variables are read at request time. A
configured `AWS_BEARER_TOKEN_BEDROCK` does not override an incoming IAM request.

For example, point the SDK at the Bedrock mount while keeping the same region
and IAM identity in both processes:

```python
import boto3

client = boto3.client(
    "bedrock-runtime",
    region_name="us-east-1",
    endpoint_url="http://127.0.0.1:8787/bedrock",
)
```

Missing or conflicting signing configuration returns HTTP 400 with code
`cave_bedrock_sigv4_configuration` before the request reaches AWS. The message
names the configuration to fix and includes no credential values. The local
proxy checks the caller's requested identity and region; it does not authenticate
the old signature as an IAM security boundary.

The proxy currently reads IAM signing credentials from those environment
variables. It does not itself load AWS profiles, invoke SSO, assume roles, or
refresh a provider credential chain. An SDK using those mechanisms must make
its current credentials available to the proxy as well. Alternatively use a
[Bedrock bearer API key](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys-use.html)
on a supported API-key route.

## Verification scope

The authentication regressions exercise the full standalone request pipeline
with a capture transport. They cover caller-versus-environment precedence,
provider isolation, Azure Bearer and opaque API keys, IAM and temporary AWS
credentials, post-transform payload signing, and rejection of unresolved
signing identities. Gemini cases cover query/header conflicts and removal of
URL credentials. A local HTTP server also checks Bedrock compressed request
bytes, encoding headers and signing. These tests do not make paid provider calls or certify every
provider endpoint, model, region or account entitlement.
