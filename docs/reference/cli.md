# CLI Commands

## Install & Uninstall

```bash
sympozium install                        # CRDs, controllers, webhook, NATS, RBAC, network policies
sympozium install --version v0.0.13      # specific version
sympozium uninstall                      # clean removal
```

## Onboarding

```bash
sympozium onboard                        # interactive setup wizard
sympozium onboard --console              # plain text fallback for CI
```

## Launch Interfaces

```bash
sympozium                                # launch the interactive TUI (default command)
sympozium serve                          # open the web dashboard in your browser
sympozium token                          # print the web dashboard authentication token
```

### `sympozium serve` Options

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9090` | Local port to forward to |
| `--open` | `false` | Automatically open a browser |
| `--service-namespace` | `sympozium-system` | Namespace of the apiserver service |

### `sympozium token`

Prints only the dashboard bearer token, which is useful after a manual
port-forward. It requires permission to read the `sympozium-ui-token` Secret.

```bash
sympozium token
sympozium token --service-namespace sympozium-system
```

## Resource Management

```bash
sympozium instances list                                # list instances
sympozium runs list                                     # list agent runs
sympozium features enable browser-automation \
  --policy default-policy                               # enable a feature gate
```

## Development

```bash
make test        # run tests
make lint        # run linter
make manifests   # generate CRD manifests
make run         # run controller locally (needs kubeconfig)
```
