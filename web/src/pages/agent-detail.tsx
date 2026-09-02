import { useState, useEffect } from "react";
import { useParams, Link, useSearchParams } from "react-router-dom";
import {
  useAgent,
  useCapabilities,
  usePatchAgent,
	useCreateHarnessSession,
	useHarnessSessions,
	useSetHarnessSessionState,
  useRuntimes,
  useRuns,
} from "@/hooks/use-api";
import { HarnessSessionChatDialog } from "@/components/harness-session-dialog";
import { StatusBadge } from "@/components/status-badge";
import { GithubAuthDialog } from "@/components/github-auth-dialog";
import {
  api,
  type SkillRef,
  type Agent,
  type AgentSandboxInstanceSpec,
  type CapabilityStatus,
  type LifecycleHooks,
  type LifecycleHookContainer,
  type SecretRef,
  type ChannelSpec,
  type ChannelStatus,
} from "@/lib/api";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertTriangle,
  MessageSquare,
  Plus,
  Pencil,
  Trash2,
  ExternalLink,
} from "lucide-react";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useRunsSeen } from "@/hooks/use-runs-seen";
import {
  costTooltip,
  effectiveCost,
  formatAge,
  formatUsd,
  sumEffectiveCosts,
  taskText,
  truncate,
} from "@/lib/utils";
import { YamlButton, instanceYamlFromResource } from "@/components/yaml-panel";

export function AgentDetailPage() {
  const { name } = useParams<{ name: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const allowedTabs = new Set([
    "overview",
    "chat",
    "harness",
    "ensemble",
    "runs",
    "channels",
    "skills",
    "memory",
    "web-endpoint",
    "lifecycle",
    "yaml",
  ]);
  const paramTab = searchParams.get("tab");
  const [activeTab, setActiveTab] = useState<string>(
    paramTab && allowedTabs.has(paramTab) ? paramTab : "overview",
  );
  const connectGithub = searchParams.get("connect") === "github";
  const { data: inst, isLoading } = useAgent(name || "");
  const { data: capabilities } = useCapabilities();
  const { data: runtimes } = useRuntimes();
  const { data: harnessSessions } = useHarnessSessions();
  const createHarnessSession = useCreateHarnessSession();
  const setHarnessSessionState = useSetHarnessSessionState();
  const [chatOpen, setChatOpen] = useState(false);
  const { data: allRuns } = useRuns();
  const { isUnseen } = useRunsSeen();
  const instanceRuns = (allRuns || [])
    .filter((r) => r.spec.agentRef === name)
    .sort(
      (a, b) =>
        new Date(b.metadata.creationTimestamp || "").getTime() -
        new Date(a.metadata.creationTimestamp || "").getTime(),
    )
    .slice(0, 20);
  const runsSpend = sumEffectiveCosts(instanceRuns);

  useEffect(() => {
    if (paramTab && allowedTabs.has(paramTab)) {
      setActiveTab(paramTab);
      return;
    }
    setActiveTab("overview");
  }, [paramTab]);

  const handleConsumeConnect = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("connect");
    setSearchParams(next, { replace: true });
  };

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!inst) {
    return <p className="text-muted-foreground">Agent not found</p>;
  }

  const selectedRuntime = runtimes?.find((runtime) => runtime.metadata.name === inst.spec.runtimeRef);
  const agentName = inst.metadata.name;
  const persistentHarness = selectedRuntime?.spec.contractVersion === "v1alpha2" && selectedRuntime.spec.session?.protocol === "openai-chat";
  const chatSession = harnessSessions?.find((session) => session.spec.agentRef === agentName && session.spec.runtimeRef === selectedRuntime?.metadata.name);
  function startChat() {
    if (!selectedRuntime) return;
    createHarnessSession.mutate({ name: defaultChatSessionName(agentName), agentRef: agentName, runtimeRef: selectedRuntime.metadata.name });
  }

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <Breadcrumbs
          items={[
            { label: "Agents", to: "/agents" },
            { label: inst.metadata.name },
          ]}
        />
        <h1 className="text-2xl font-bold font-mono">{inst.metadata.name}</h1>
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          Created {formatAge(inst.metadata.creationTimestamp)} ago
          <StatusBadge phase={inst.status?.phase} />
        </p>
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          {persistentHarness && <TabsTrigger value="chat"><MessageSquare className="mr-1 h-3.5 w-3.5" />Chat</TabsTrigger>}
          <TabsTrigger value="harness">Harness</TabsTrigger>
          <TabsTrigger value="ensemble">Ensemble</TabsTrigger>
          <TabsTrigger value="runs">
            Runs{instanceRuns.length > 0 ? ` (${instanceRuns.length})` : ""}
          </TabsTrigger>
          <TabsTrigger value="channels">Channels</TabsTrigger>
          <TabsTrigger value="skills">Skills</TabsTrigger>
          <TabsTrigger value="memory">Memory</TabsTrigger>
          <TabsTrigger value="web-endpoint">Web Endpoint</TabsTrigger>
          <TabsTrigger value="lifecycle">Lifecycle</TabsTrigger>
          <TabsTrigger value="yaml">YAML</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <div className="grid gap-4 md:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Agent Configuration</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                <Row label="Model" value={inst.spec.agents?.default?.model} />
                <Row
                  label="Base URL"
                  value={inst.spec.agents?.default?.baseURL}
                />
                <Row
                  label="Thinking"
                  value={inst.spec.agents?.default?.thinking}
                />
                <Row label="Policy" value={inst.spec.policyRef} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">Status</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                <Row label="Phase" value={inst.status?.phase} />
                <Row
                  label="Active Pods"
                  value={String(inst.status?.activeAgentPods ?? 0)}
                />
                <Row
                  label="Total Runs"
                  value={String(inst.status?.totalAgentRuns ?? 0)}
                />
              </CardContent>
            </Card>

            {inst.spec.authRefs && inst.spec.authRefs.length > 0 && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">Auth References</CardTitle>
                </CardHeader>
                <CardContent>
                  <div className="space-y-2">
                    {inst.spec.authRefs.map((ref: SecretRef, i: number) => (
                      <div key={i} className="flex items-center gap-2 text-sm">
                        <Badge variant="secondary">{ref.provider}</Badge>
                        <span className="font-mono text-muted-foreground">
                          {ref.secret}
                        </span>
                      </div>
                    ))}
                  </div>
                </CardContent>
              </Card>
            )}

            <AgentSandboxCard
              sandbox={inst.spec.agents?.default?.agentSandbox}
              capability={capabilities?.agentSandbox}
            />

            <ResponseGateCard inst={inst} />
          </div>
        </TabsContent>

        {persistentHarness && <TabsContent value="chat">
          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2 text-base"><MessageSquare className="h-4 w-4" />Persistent chat</CardTitle></CardHeader>
            <CardContent className="space-y-4 text-sm">
              <p className="text-muted-foreground">This Agent’s harness is a durable conversation pod. Chat turns stay in that harness; use the Runs tab only for explicit one-shot or workflow executions.</p>
              {!chatSession ? <Button onClick={startChat} disabled={createHarnessSession.isPending}>{createHarnessSession.isPending ? "Requesting chat…" : "Retry session creation"}</Button> : chatSession.status?.phase === "Ready" ? <div className="flex items-center justify-between rounded border p-3"><div><p className="font-mono text-xs">{chatSession.metadata.name}</p><p className="text-xs text-muted-foreground">Ready · durable {selectedRuntime.metadata.name} conversation</p></div><div className="flex gap-2"><Button variant="outline" onClick={() => setHarnessSessionState.mutate({ name: chatSession.metadata.name, desiredState: "stopped" })} disabled={setHarnessSessionState.isPending}>Stop</Button><Button onClick={() => setChatOpen(true)}><MessageSquare className="mr-2 h-4 w-4" />Open chat</Button></div></div> : chatSession.status?.phase === "Failed" ? <div className="rounded border border-destructive/50 bg-destructive/5 p-3"><p className="font-medium text-destructive">Persistent chat failed to start</p><p className="mt-1 text-xs text-muted-foreground">{chatSession.status.conditions?.find((condition) => condition.type === "Ready")?.message || "The session controller did not provide a failure reason."}</p><div className="mt-3 flex gap-2"><Button variant="outline" onClick={() => setHarnessSessionState.mutate({ name: chatSession.metadata.name, desiredState: "running" })} disabled={setHarnessSessionState.isPending}>Retry</Button></div></div> : chatSession.spec.desiredState === "stopped" || chatSession.status?.phase === "Draining" ? <div className="flex items-center justify-between rounded border p-3"><div><p className="font-mono text-xs">{chatSession.metadata.name}</p><p className="text-xs text-muted-foreground">Chat is stopped. Resume it when you want to continue.</p></div><Button onClick={() => setHarnessSessionState.mutate({ name: chatSession.metadata.name, desiredState: "running" })} disabled={setHarnessSessionState.isPending}>Resume chat</Button></div> : <div className="rounded border p-3 text-muted-foreground">Starting persistent chat session… <span className="font-mono">{chatSession.status?.phase || "Pending"}</span></div>}
            </CardContent>
          </Card>
        </TabsContent>}

        <TabsContent value="harness">
          <div className="space-y-4">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">How this Agent executes</CardTitle>
              </CardHeader>
              <CardContent className="text-sm text-muted-foreground">
                A harness supplies the agent loop. Sympozium still owns the
                AgentRun lifecycle, policy, per-run identity, skills, memory,
                MCP integrations, and execution audit. Leave the runtime on
                <span className="font-medium text-foreground"> Built-in agent-runner</span>
                to preserve the native execution path.
              </CardContent>
            </Card>
            <AgentRuntimeCard inst={inst} runtimes={runtimes || []} />
          </div>
        </TabsContent>

        <TabsContent value="ensemble">
          <AgentEnsembleCard inst={inst} />
        </TabsContent>

        <TabsContent value="runs">
          <Card>
            <CardContent className="pt-6">
              {instanceRuns.length > 0 ? (
                <div className="space-y-2">
                  {runsSpend.count > 0 && (
                    <div
                      className="flex justify-end text-xs text-muted-foreground"
                      title={
                        runsSpend.anySimulated
                          ? "Estimated spend for the runs listed — includes simulated rates"
                          : "Estimated spend for the runs listed"
                      }
                    >
                      total {formatUsd(runsSpend.totalMicro)}
                    </div>
                  )}
                  {instanceRuns.map((run) => {
                    const est = effectiveCost(run);
                    return (
                    <Link
                      key={run.metadata.name}
                      to={`/runs/${run.metadata.name}`}
                      className="flex items-center justify-between rounded-lg border p-3 hover:bg-white/5 transition-colors"
                    >
                      <div className="flex items-center gap-3 min-w-0">
                        {isUnseen(run) && (
                          <span
                            className="h-2 w-2 rounded-full bg-blue-500 shrink-0"
                            title="New"
                          />
                        )}
                        <StatusBadge phase={run.status?.phase} />
                        <span className="font-mono text-xs truncate">
                          {run.metadata.name}
                        </span>
                        <span className="text-xs text-muted-foreground truncate max-w-xs hidden sm:inline">
                          {truncate(taskText(run.spec.task), 50)}
                        </span>
                      </div>
                      <div className="flex items-center gap-3 shrink-0">
                        {run.status?.tokenUsage && (
                          <span className="text-xs text-muted-foreground">
                            {run.status.tokenUsage.totalTokens.toLocaleString()}{" "}
                            tokens
                          </span>
                        )}
                        {est && (
                          <span
                            className="text-xs text-muted-foreground"
                            title={costTooltip(est)}
                          >
                            {formatUsd(est.amountMicro)}
                          </span>
                        )}
                        <span className="text-xs text-muted-foreground">
                          {formatAge(run.metadata.creationTimestamp)}
                        </span>
                        <ExternalLink className="h-3 w-3 text-muted-foreground" />
                      </div>
                    </Link>
                    );
                  })}
                  {(allRuns || []).filter((r) => r.spec.agentRef === name)
                    .length > 20 && (
                    <Link
                      to={`/runs?search=${name}`}
                      className="block text-center text-xs text-blue-400 hover:text-blue-300 py-2"
                    >
                      View all runs
                    </Link>
                  )}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No runs yet. Dispatch an ad-hoc run from the{" "}
                  <Link
                    to="/runs"
                    className="text-blue-400 hover:text-blue-300"
                  >
                    Runs page
                  </Link>
                  .
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="channels">
          <Card>
            <CardContent className="pt-6">
              {inst.spec.channels && inst.spec.channels.length > 0 ? (
                <div className="space-y-3">
                  {inst.spec.channels.map((ch: ChannelSpec, i: number) => {
                    const chStatus = inst.status?.channels?.find(
                      (s: ChannelStatus) => s.type === ch.type,
                    );
                    return (
                      <div
                        key={i}
                        className="flex items-center justify-between rounded-lg border p-3"
                      >
                        <div className="flex items-center gap-3">
                          <Badge variant="outline" className="capitalize">
                            {ch.type}
                          </Badge>
                          {ch.configRef && (
                            <span className="text-xs text-muted-foreground font-mono">
                              secret: {ch.configRef.secret}
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-2">
                          <StatusBadge phase={chStatus?.status} />
                          {chStatus?.message && (
                            <span className="text-xs text-muted-foreground">
                              {chStatus.message}
                            </span>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No channels configured
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="skills">
          <SkillsTab
            skills={inst.spec.skills}
            autoOpenGithubAuth={connectGithub}
            onConsumeGithubPrompt={handleConsumeConnect}
          />
        </TabsContent>

        <TabsContent value="memory">
          <Card>
            <CardContent className="pt-6">
              {inst.spec.memory ? (
                <div className="space-y-3">
                  <Row
                    label="Enabled"
                    value={inst.spec.memory.enabled ? "Yes" : "No"}
                  />
                  <Row
                    label="Max Size"
                    value={
                      inst.spec.memory.maxSizeKB
                        ? `${inst.spec.memory.maxSizeKB} KB`
                        : "Default"
                    }
                  />
                  <Separator />
                  {inst.spec.memory.systemPrompt && (
                    <div>
                      <p className="text-sm font-medium mb-2">System Prompt</p>
                      <pre className="rounded bg-muted/50 p-3 text-xs whitespace-pre-wrap">
                        {inst.spec.memory.systemPrompt}
                      </pre>
                    </div>
                  )}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Memory not configured
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="web-endpoint">
          <WebEndpointTab inst={inst} />
        </TabsContent>

        <TabsContent value="lifecycle">
          <LifecycleTab
            agentName={inst.metadata.name}
            lifecycle={inst.spec.agents?.default?.lifecycle}
          />
        </TabsContent>

        <TabsContent value="yaml">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Resource YAML</CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-sm text-muted-foreground mb-3">
                View the equivalent Agent manifest for this
                resource.
              </p>
              <YamlButton
                yaml={instanceYamlFromResource(inst)}
                title={`Agent — ${inst.metadata.name}`}
              />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
      {chatSession && <HarnessSessionChatDialog open={chatOpen} onOpenChange={setChatOpen} session={chatSession} />}
    </div>
  );
}

function defaultChatSessionName(agentName: string) {
  return `${agentName.slice(0, 57).replace(/-+$/, "")}-chat`;
}

function AgentRuntimeCard({ inst, runtimes }: { inst: Agent; runtimes: import("@/lib/api").AgentRuntime[] }) {
  const patchAgent = usePatchAgent();
  const selected = inst.spec.runtimeRef || "none";

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Agent Harness Runtime</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Administrator-approved runtime inherited by channels, schedules, and ordinary AgentRuns. A New Run choice can override it once.
        </p>
        <Select
          value={selected}
          onValueChange={(value) => patchAgent.mutate({
            name: inst.metadata.name,
            data: { runtimeRef: value === "none" ? "" : value },
          })}
          disabled={patchAgent.isPending}
        >
          <SelectTrigger>
            <SelectValue placeholder="Built-in agent-runner" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Built-in agent-runner</SelectItem>
            {runtimes.map((runtime) => (
              <SelectItem key={runtime.metadata.name} value={runtime.metadata.name}>
                {runtime.metadata.name}{runtime.spec.supportOwner ? ` — ${runtime.spec.supportOwner}` : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </CardContent>
    </Card>
  );
}

function AgentEnsembleCard({ inst }: { inst: Agent }) {
  const ensembleName = inst.metadata.labels?.["sympozium.ai/ensemble"];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Ensemble membership</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {ensembleName ? (
          <>
            <p className="text-muted-foreground">
              This Agent was created and is coordinated by the following Ensemble.
              Its runtime remains a separate administrator-owned execution choice.
            </p>
            <Link
              to={`/ensembles/${ensembleName}`}
              className="inline-flex items-center gap-2 rounded-md border px-3 py-2 font-mono text-blue-400 hover:bg-white/5"
            >
              {ensembleName}
              <ExternalLink className="h-3.5 w-3.5" />
            </Link>
          </>
        ) : (
          <>
            <p className="text-muted-foreground">
              This is a standalone Agent. It is not currently managed by an Ensemble.
            </p>
            <Link to="/ensembles" className="text-blue-400 hover:text-blue-300">
              Browse Ensembles
            </Link>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function AgentSandboxCard({
  sandbox,
  capability,
}: {
  sandbox?: AgentSandboxInstanceSpec;
  capability?: CapabilityStatus;
}) {
  const crdInstalled = capability?.available ?? false;
  const enabled = crdInstalled && sandbox?.enabled === true;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Agent Sandbox</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {!crdInstalled ? (
          <div className="flex items-start gap-2 rounded-lg border border-yellow-500/30 bg-yellow-500/5 p-3">
            <AlertTriangle className="h-4 w-4 mt-0.5 text-yellow-600 shrink-0" />
            <div className="text-sm">
              <p className="font-medium text-yellow-600">Unavailable</p>
              <p className="text-muted-foreground">
                {capability?.reason ||
                  "Agent Sandbox CRDs are not installed in the cluster."}
              </p>
            </div>
          </div>
        ) : (
          <>
            <Row label="Enabled" value={enabled ? "Yes" : "No"} />
            {enabled && (
              <>
                <Row
                  label="Runtime Class"
                  value={sandbox?.runtimeClass || "default"}
                />
                {sandbox?.warmPool && (
                  <>
                    <Separator />
                    <p className="text-xs font-medium text-muted-foreground">
                      Warm Pool
                    </p>
                    <Row
                      label="Size"
                      value={String(sandbox.warmPool.size ?? 2)}
                    />
                    {sandbox.warmPool.runtimeClass && (
                      <Row
                        label="Runtime Class"
                        value={sandbox.warmPool.runtimeClass}
                      />
                    )}
                  </>
                )}
              </>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function ResponseGateCard({ inst }: { inst: Agent }) {
  const patchInstance = usePatchAgent();
  const enabled =
    inst.spec.agents?.default?.lifecycle?.postRun?.some((h) => h.gate) ?? false;

  function toggle() {
    patchInstance.mutate({
      name: inst.metadata.name,
      data: { requireApproval: !enabled },
    });
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Response Gate</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">
              {enabled ? "Enabled" : "Disabled"}
            </p>
            <p className="text-xs text-muted-foreground">
              {enabled
                ? "All runs require manual approval before the response reaches users."
                : "Responses are delivered immediately after the agent completes."}
            </p>
          </div>
          <Button
            variant={enabled ? "destructive" : "default"}
            size="sm"
            data-testid="gate-toggle-btn"
            onClick={toggle}
            disabled={patchInstance.isPending}
          >
            {patchInstance.isPending
              ? "Saving..."
              : enabled
                ? "Disable"
                : "Enable"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function WebEndpointTab({ inst }: { inst: Agent }) {
  const webSkill = inst.spec.skills?.find(
    (s) =>
      s.skillPackRef === "web-endpoint" ||
      s.skillPackRef === "skillpack-web-endpoint",
  );

  if (webSkill) {
    return (
      <Card>
        <CardContent className="pt-6 space-y-3">
          <Row
            label="Rate Limit"
            value={`${webSkill.params?.rate_limit_rpm || "60"} req/min`}
          />
          <Row
            label="Hostname"
            value={webSkill.params?.hostname || "auto from gateway"}
          />
          <Separator />
          <p className="text-sm font-medium">Status</p>
          <p className="text-xs text-muted-foreground">
            The web-proxy runs as a server-mode AgentRun. Check the Runs page
            for a run in "Serving" phase with a Deployment and Service.
          </p>
        </CardContent>
      </Card>
    );
  }

  // Not enabled
  return (
    <Card>
      <CardContent className="pt-6">
        <p className="text-sm text-muted-foreground">
          Web endpoint is not enabled. Add the "web-endpoint" skill to expose
          this agent as an HTTP API.
        </p>
      </CardContent>
    </Card>
  );
}

function Row({ label, value }: { label: string; value?: string | null }) {
  return (
    <div className="flex items-center justify-between text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono">{value || "—"}</span>
    </div>
  );
}

function SkillsTab({
  skills,
  autoOpenGithubAuth,
  onConsumeGithubPrompt,
}: {
  skills?: SkillRef[];
  autoOpenGithubAuth?: boolean;
  onConsumeGithubPrompt?: () => void;
}) {
  const [authDialogOpen, setAuthDialogOpen] = useState(false);
  const [authStatus, setAuthStatus] = useState<string | null>(null);

  const hasGithubGitops = skills?.some(
    (sk) => sk.skillPackRef === "github-gitops",
  );
  const ghSkill = skills?.find((sk) => sk.skillPackRef === "github-gitops");

  // Check auth status when github-gitops is attached
  useEffect(() => {
    if (!hasGithubGitops) return;
    let cancelled = false;
    const check = async () => {
      try {
        const res = await api.githubAuth.status();
        if (!cancelled) setAuthStatus(res.status);
      } catch {
        if (!cancelled) setAuthStatus("unknown");
      }
    };
    check();
    const interval = setInterval(check, 15000);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [hasGithubGitops]);

  useEffect(() => {
    if (!autoOpenGithubAuth || !hasGithubGitops) return;
    setAuthDialogOpen(true);
    onConsumeGithubPrompt?.();
  }, [autoOpenGithubAuth, hasGithubGitops, onConsumeGithubPrompt]);

  return (
    <>
      <Card>
        <CardContent className="pt-6 space-y-4">
          {skills && skills.length > 0 ? (
            <div className="flex flex-wrap gap-2">
              {skills.map((sk, i) => (
                <Badge key={i} variant="secondary">
                  {sk.skillPackRef}
                  {sk.params?.repo && (
                    <span className="ml-1 text-xs text-muted-foreground">
                      → {sk.params.repo}
                    </span>
                  )}
                </Badge>
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">No skills attached</p>
          )}

          {hasGithubGitops && (
            <div
              className={`rounded-lg border p-4 ${
                authStatus === "complete"
                  ? "border-green-500/30 bg-green-500/5"
                  : "border-yellow-500/30 bg-yellow-500/5"
              }`}
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3">
                  <svg
                    className="h-5 w-5"
                    viewBox="0 0 24 24"
                    fill="currentColor"
                    aria-hidden="true"
                  >
                    <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
                  </svg>
                  <div>
                    <p className="text-sm font-medium">GitHub GitOps</p>
                    {ghSkill?.params?.repo && (
                      <p className="text-xs text-muted-foreground">
                        {ghSkill.params.repo}
                      </p>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  {authStatus === "complete" ? (
                    <Badge
                      variant="outline"
                      className="border-green-500/50 text-green-600"
                    >
                      Authenticated
                    </Badge>
                  ) : authStatus === "pending" ? (
                    <Badge
                      variant="outline"
                      className="border-yellow-500/50 text-yellow-600"
                    >
                      Awaiting auth…
                    </Badge>
                  ) : (
                    <button
                      onClick={() => setAuthDialogOpen(true)}
                      className="inline-flex items-center gap-1.5 rounded-md border border-yellow-500/50 bg-yellow-500/10 px-3 py-1.5 text-xs font-medium text-yellow-600 transition-colors hover:bg-yellow-500/20"
                    >
                      Connect GitHub
                    </button>
                  )}
                </div>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <GithubAuthDialog
        open={authDialogOpen}
        onClose={() => setAuthDialogOpen(false)}
      />
    </>
  );
}

const lifecycleEnvVars = [
  { name: "AGENT_RUN_ID", desc: "Unique run identifier", scope: "all" },
  { name: "INSTANCE_NAME", desc: "Agent name", scope: "all" },
  { name: "AGENT_NAMESPACE", desc: "Kubernetes namespace", scope: "all" },
  {
    name: "AGENT_EXIT_CODE",
    desc: "Agent container exit code",
    scope: "postRun",
  },
  {
    name: "AGENT_RESULT",
    desc: "Agent response (truncated to 32Ki)",
    scope: "postRun",
  },
];

const emptyHook: LifecycleHookContainer = {
  name: "",
  image: "",
  command: [],
  env: [],
};

function LifecycleTab({
  agentName,
  lifecycle,
}: {
  agentName: string;
  lifecycle?: LifecycleHooks;
}) {
  const patchMutation = usePatchAgent();
  const [editingHook, setEditingHook] = useState<{
    hook: LifecycleHookContainer;
    phase: "preRun" | "postRun";
    index: number;
  } | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);

  const preRun = lifecycle?.preRun ?? [];
  const postRun = lifecycle?.postRun ?? [];
  const rbac = lifecycle?.rbac ?? [];

  const saveLifecycle = (updated: LifecycleHooks) => {
    patchMutation.mutate({ name: agentName, data: { lifecycle: updated } });
  };

  const openAddHook = (phase: "preRun" | "postRun") => {
    setEditingHook({ hook: { ...emptyHook }, phase, index: -1 });
    setDialogOpen(true);
  };

  const openEditHook = (phase: "preRun" | "postRun", index: number) => {
    const hooks = phase === "preRun" ? preRun : postRun;
    setEditingHook({
      hook: {
        ...hooks[index],
        command: [...(hooks[index].command ?? [])],
        env: [...(hooks[index].env ?? [])],
      },
      phase,
      index,
    });
    setDialogOpen(true);
  };

  const deleteHook = (phase: "preRun" | "postRun", index: number) => {
    const updated: LifecycleHooks = {
      preRun: [...preRun],
      postRun: [...postRun],
      rbac: [...rbac],
    };
    if (phase === "preRun") {
      updated.preRun = preRun.filter((_, i) => i !== index);
    } else {
      updated.postRun = postRun.filter((_, i) => i !== index);
    }
    saveLifecycle(updated);
  };

  const saveHook = (hook: LifecycleHookContainer) => {
    if (!editingHook) return;
    const updated: LifecycleHooks = {
      preRun: [...preRun],
      postRun: [...postRun],
      rbac: [...rbac],
    };
    const list =
      editingHook.phase === "preRun" ? updated.preRun! : updated.postRun!;
    if (editingHook.index === -1) {
      list.push(hook);
    } else {
      list[editingHook.index] = hook;
    }
    saveLifecycle(updated);
    setDialogOpen(false);
    setEditingHook(null);
  };

  return (
    <div className="space-y-4">
      <HookSection
        title="PreRun Hooks"
        description="Execute as init containers before the agent starts."
        hooks={preRun}
        onAdd={() => openAddHook("preRun")}
        onEdit={(i) => openEditHook("preRun", i)}
        onDelete={(i) => deleteHook("preRun", i)}
      />

      <HookSection
        title="PostRun Hooks"
        description="Execute in a follow-up Job after the agent completes."
        hooks={postRun}
        onAdd={() => openAddHook("postRun")}
        onEdit={(i) => openEditHook("postRun", i)}
        onDelete={(i) => deleteHook("postRun", i)}
      />

      {rbac.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">RBAC Rules</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              {rbac.map((rule, i) => (
                <div
                  key={i}
                  className="flex items-center gap-2 text-sm font-mono"
                >
                  <Badge variant="secondary">
                    {rule.apiGroups.map((g) => g || "core").join(", ")}
                  </Badge>
                  <span className="text-muted-foreground">
                    {rule.resources.join(", ")}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    [{rule.verbs.join(", ")}]
                  </span>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            Available Environment Variables
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-1">
            {lifecycleEnvVars.map((ev) => (
              <div key={ev.name} className="flex items-baseline gap-3 text-sm">
                <code className="font-mono text-xs bg-muted/50 px-1.5 py-0.5 rounded min-w-[10rem]">
                  {ev.name}
                </code>
                <span className="text-muted-foreground">{ev.desc}</span>
                {ev.scope === "postRun" && (
                  <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                    postRun only
                  </Badge>
                )}
              </div>
            ))}
            <p className="text-xs text-muted-foreground mt-3">
              Custom env vars from <code className="font-mono">spec.env</code>{" "}
              are also forwarded to all hook containers.
            </p>
          </div>
        </CardContent>
      </Card>

      <HookEditDialog
        open={dialogOpen}
        onOpenChange={(open) => {
          setDialogOpen(open);
          if (!open) setEditingHook(null);
        }}
        hook={editingHook?.hook ?? emptyHook}
        phase={editingHook?.phase ?? "preRun"}
        isNew={editingHook?.index === -1}
        onSave={saveHook}
      />
    </div>
  );
}

function HookSection({
  title,
  description,
  hooks,
  onAdd,
  onEdit,
  onDelete,
}: {
  title: string;
  description: string;
  hooks: LifecycleHookContainer[];
  onAdd: () => void;
  onEdit: (index: number) => void;
  onDelete: (index: number) => void;
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="text-base">{title}</CardTitle>
        <Button variant="outline" size="sm" onClick={onAdd}>
          <Plus className="h-3.5 w-3.5 mr-1" /> Add
        </Button>
      </CardHeader>
      <CardContent>
        {hooks.length > 0 ? (
          <div className="space-y-3">
            {hooks.map((hook, i) => (
              <div key={i} className="rounded-lg border p-3 space-y-2">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm">{hook.name}</span>
                    <Badge variant="secondary" className="font-mono text-xs">
                      {hook.image}
                    </Badge>
                  </div>
                  <div className="flex gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0"
                      onClick={() => onEdit(i)}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0 text-destructive hover:text-destructive"
                      onClick={() => onDelete(i)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
                {hook.command && hook.command.length > 0 && (
                  <div className="text-xs">
                    <span className="text-muted-foreground">Command: </span>
                    <code className="font-mono bg-muted/50 px-1 py-0.5 rounded">
                      {hook.command.join(" ")}
                    </code>
                  </div>
                )}
                {hook.env && hook.env.length > 0 && (
                  <div className="text-xs space-y-0.5">
                    <span className="text-muted-foreground">Env:</span>
                    {hook.env.map((e, j) => (
                      <div key={j} className="ml-2 font-mono">
                        <span className="text-muted-foreground">{e.name}</span>=
                        <span>{e.value}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            No hooks configured. {description}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function HookEditDialog({
  open,
  onOpenChange,
  hook,
  phase,
  isNew,
  onSave,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  hook: LifecycleHookContainer;
  phase: "preRun" | "postRun";
  isNew: boolean;
  onSave: (hook: LifecycleHookContainer) => void;
}) {
  const [name, setName] = useState(hook.name);
  const [image, setImage] = useState(hook.image);
  const [command, setCommand] = useState(hook.command?.join(" ") ?? "");
  const [envText, setEnvText] = useState(
    hook.env?.map((e) => `${e.name}=${e.value}`).join("\n") ?? "",
  );

  // Reset form when hook changes.
  useEffect(() => {
    setName(hook.name);
    setImage(hook.image);
    setCommand(hook.command?.join(" ") ?? "");
    setEnvText(hook.env?.map((e) => `${e.name}=${e.value}`).join("\n") ?? "");
  }, [hook]);

  const handleSave = () => {
    const envParsed = envText
      .split("\n")
      .filter((l) => l.includes("="))
      .map((l) => {
        const [k, ...v] = l.split("=");
        return { name: k.trim(), value: v.join("=") };
      });
    onSave({
      name: name.trim(),
      image: image.trim(),
      command: command.trim() ? command.trim().split(/\s+/) : undefined,
      env: envParsed.length > 0 ? envParsed : undefined,
    });
  };

  const valid = name.trim() !== "" && image.trim() !== "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {isNew ? "Add" : "Edit"} {phase === "preRun" ? "PreRun" : "PostRun"}{" "}
            Hook
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-4 pt-2">
          <div className="space-y-2">
            <Label htmlFor="hook-name">Name</Label>
            <Input
              id="hook-name"
              placeholder="e.g. fetch-context"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="hook-image">Image</Label>
            <Input
              id="hook-image"
              placeholder="e.g. curlimages/curl:latest"
              value={image}
              onChange={(e) => setImage(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="hook-command">Command</Label>
            <Input
              id="hook-command"
              placeholder="e.g. sh -c 'curl http://...'"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Space-separated. Leave empty to use the image's default
              entrypoint.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="hook-env">Environment Variables</Label>
            <Textarea
              id="hook-env"
              placeholder={"KEY=VALUE\nANOTHER=value"}
              value={envText}
              onChange={(e) => setEnvText(e.target.value)}
              rows={3}
              className="font-mono text-xs"
            />
            <p className="text-xs text-muted-foreground">
              One per line, KEY=VALUE format.
            </p>
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={!valid}>
              {isNew ? "Add Hook" : "Save"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
