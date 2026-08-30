# AWS Bedrock Guide

This guide covers setting up and using Amazon Bedrock as an LLM provider in Sympozium.

## Overview

Amazon Bedrock provides access to foundation models from Amazon (Nova), Anthropic (Claude), AI21, Cohere, and Meta through a managed API. Sympozium supports Bedrock's Converse API for conversational interactions with tool calling.

## Prerequisites

1. **AWS Account** with Bedrock access enabled
2. **Bedrock Model Access**: Some models require explicit approval in the AWS Console
3. **Kubernetes cluster** with Sympozium installed

## Step 1: Enable Bedrock Models

### Console Method
1. Navigate to [AWS Bedrock Console](https://console.aws.amazon.com/bedrock)
2. Go to **Model access** in the left sidebar
3. Click **Manage model access**
4. Select the models you want to use:
   - **Anthropic Claude**: `anthropic.claude-sonnet-4-20250514-v1:0`, `anthropic.claude-haiku-4-5-20251001-v1:0`, etc.
   - **Amazon Nova**: `amazon.nova-pro-v1:0`, `amazon.nova-lite-v1:0`
5. Check the boxes and click **Confirm changes**

### AWS CLI Method
```bash
# List available models (requires bedrock:ListFoundationModels permission)
aws bedrock list-foundation-models --region us-east-1 \
  --by-provider Anthropic \
  --query 'modelSummaries[*].{id:modelId,ar:arn}' \
  --output table
```

## Step 2: Configure Authentication

You have three options for authentication:

### Option A: IAM User Access Keys (Simplest)

1. Create an IAM user with Bedrock permissions
2. Generate access keys
3. Use them directly in Kubernetes secrets

**IAM Policy:**
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "bedrock:ListFoundationModels",
        "bedrock:GetFoundationModel",
        "bedrock:Converse",
        "bedrock:ConverseStream",
        "bedrock:InvokeModel"
      ],
      "Resource": "*"
    }
  ]
}
```

### Option B: IRSA (EKS IAM Roles for Service Accounts)

For EKS clusters, use IRSA to avoid managing access keys:

1. **Create an IAM OIDC provider** (if not exists):
```bash
eksctl utils associate-iam-oidc-provider \
  --cluster <cluster-name> \
  --approve
```

2. **Create IAM role with trust policy:**
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::<account-id>:oidc-provider/oidc.eks.<region>.amazonaws.com/id/<oidc-id>"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringLike": {
          "oidc.eks.<region>.amazonaws.com/id/<oidc-id>:sub": "system:serviceaccount:sympozium:sympozium-run-*"
        }
      }
    }
  ]
}
```

3. **Attach Bedrock permissions policy** to the role (same as Option A)

4. **Annotate the namespace identity template:**
```bash
kubectl annotate serviceaccount sympozium-agent \
  eks.amazonaws.com/role-arn=arn:aws:iam::<account-id>:role/<bedrock-role-name> \
  -n sympozium
```

Each AgentRun uses a unique `sympozium-run-<run>` ServiceAccount. The controller
copies workload-identity annotations from `sympozium-agent` when it creates that
identity, while the IAM trust policy above limits federation to run accounts in
this namespace. The base account is an annotation template and is not selected
by AgentRun pods.

### Option C: EC2 Instance Profile / EKS Node Role

If running on EC2/EKS nodes with an instance profile:
- The node's IAM role will be used automatically
- Ensure the role has Bedrock permissions (see Option A policy)

## Step 3: Create Kubernetes Secret

The `AWS_REGION` key in the secret tells Sympozium which Bedrock region to use. This must match the region where you enabled model access.

### For IAM User Credentials
```bash
kubectl create secret generic my-instance-bedrock-key -n sympozium \
  --from-literal=AWS_REGION=us-east-1 \
  --from-literal=AWS_ACCESS_KEY_ID=<your-access-key-id> \
  --from-literal=AWS_SECRET_ACCESS_KEY=<your-secret-access-key>
```

### For IRSA (No Access Keys)
```bash
kubectl create secret generic my-instance-bedrock-key -n sympozium \
  --from-literal=AWS_REGION=us-east-1
```

### For Temporary Credentials (STS)
If using STS temporary credentials:
```bash
kubectl create secret generic my-instance-bedrock-key -n sympozium \
  --from-literal=AWS_REGION=us-east-1 \
  --from-literal=AWS_ACCESS_KEY_ID=<access-key> \
  --from-literal=AWS_SECRET_ACCESS_KEY=<secret-key> \
  --from-literal=AWS_SESSION_TOKEN=<session-token>
```

## Step 4: Create Agent

### Using the CLI Wizard
```bash
sympozium create instance my-bedrock-agent
# Select option 6: AWS Bedrock (Claude, Nova, etc.)
# Enter your region and model ID when prompted
```

### Manual YAML Configuration
```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-bedrock-agent
  namespace: sympozium
spec:
  authRefs:
    - provider: bedrock
      secret: my-instance-bedrock-key
  agents:
    default:
      model: anthropic.claude-sonnet-4-20250514-v1:0
  # Optional: Enable specific skills
  skills:
    - skillPackRef: k8s-ops
  # Optional: Channel bindings (Telegram, Slack, etc.)
  channels:
    - type: telegram
      configRef:
        secret: my-telegram-creds
```

## Available Bedrock Models

Sympozium supports all text-capable foundation models. Common options:

| Provider | Model ID | Description |
|----------|----------|-------------|
| Anthropic | `anthropic.claude-sonnet-4-20250514-v1:0` | Best balanced performance |
| Anthropic | `anthropic.claude-haiku-4-5-20251001-v1:0` | Fast and cost-efficient |
| Anthropic | `anthropic.claude-3-5-sonnet-20241022-v2:0` | Claude 3.5 Sonnet |
| Amazon | `amazon.nova-pro-v1:0` | Amazon's Nova Pro |
| Amazon | `amazon.nova-lite-v1:0` | Amazon's Nova Lite |

Run `aws bedrock list-foundation-models` to see all models available in your region.

### Listing Available Models via Sympozium API

This endpoint is exposed by the Sympozium API server inside the cluster:

```bash
curl -X POST http://<apiserver>/api/v1/providers/bedrock/models \
  -H "Content-Type: application/json" \
  -d '{"region": "us-east-1", "accessKeyId": "<key>", "secretAccessKey": "<secret>"}'
```

## Tool Calling with Bedrock

Bedrock's Converse API supports native tool calling. Sympozium automatically:
1. Converts tool definitions to Bedrock's `ToolSpecification` format
2. Handles the multi-turn conversation flow for tool execution
3. Formats tool results back to the model

Example workflow:
```
User task → Model generates tool_use block → Sympozium executes tool →
Model receives tool_result → Final response
```

## Troubleshooting

### AccessDeniedException
- Verify the model is approved in Bedrock Console
- Check IAM permissions include `bedrock:Converse` and `bedrock:ListFoundationModels`
- Ensure the `AWS_REGION` in your secret matches where models are provisioned

### ModelNotAccessibleException
- Some models require explicit opt-in in specific regions
- Try a different region or model ID

### Credentials not discovered
- Verify secret contains: `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- For IRSA, check service account annotations and IAM role trust policy
- Check agent pod logs: `kubectl logs -l sympozium.ai/instance=<name> -n sympozium`

### Region mismatch
- Bedrock models are region-specific
- Ensure `AWS_REGION` in your Kubernetes secret matches where you approved model access

## Best Practices

1. **Use IRSA for production**: Avoid long-lived access keys
2. **Least privilege IAM**: Restrict to specific model ARNs when possible
3. **Model selection**: Start with `anthropic.claude-haiku-4-5-20251001-v1:0` for cost efficiency
4. **Region consistency**: Keep `AWS_REGION` in your secret consistent with where you approved model access
5. **Monitor costs**: Bedrock charges per input/output token

## Example: Full Deployment

```bash
# 1. Create IAM role and policy (IRSA)
eksctl create iamserviceaccount \
  --name sympozium-agent \
  --namespace sympozium \
  --cluster <cluster> \
  --role-name sympozium-bedrock-role \
  --attach-policy-arn arn:aws:iam::<account>:policy/BedrockAccess \
  --approve

# Update the generated role trust condition from StringEquals on
# sympozium-agent to StringLike on sympozium-run-* as shown above.

# 2. Create minimal secret (IRSA handles auth)
kubectl create secret generic bedrock-agent-key -n sympozium \
  --from-literal=AWS_REGION=us-east-1

# 3. Deploy Agent
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: bedrock-assistant
  namespace: sympozium
spec:
  authRefs:
    - provider: bedrock
      secret: bedrock-agent-key
  agents:
    default:
      model: anthropic.claude-sonnet-4-20250514-v1:0
  skills:
    - skillPackRef: k8s-ops
  memory:
    enabled: true
    maxSizeKB: 256
EOF

# 4. Test with a simple AgentRun
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: test-bedrock
  namespace: sympozium
spec:
  agentRef: bedrock-assistant
  agentId: default
  sessionKey: "test-bedrock-001"
  task: "Say hello in one sentence"
  model:
    provider: bedrock
    model: anthropic.claude-sonnet-4-20250514-v1:0
    authSecretRef: my-instance-bedrock-key
EOF

# 5. Check results
kubectl get agent bedrock-assistant -n sympozium
kubectl get agentrun test-bedrock -n sympozium -o yaml
```

## Related Documentation

- [Sympozium Design](/docs/design.md) - Architecture overview
- [Writing Skills](/guides/writing-skills.md) - Creating custom skills
- [Channels](/concepts/channels.md) - Connecting Telegram, Slack, etc.
