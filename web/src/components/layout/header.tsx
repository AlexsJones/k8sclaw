import { useAuth } from "@/components/auth-provider";
import { getNamespace, setNamespace } from "@/lib/api";
import { useNamespaces, useCanaryConfig, useRuntimes, useInstallDefaultRuntimes } from "@/hooks/use-api";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Activity, LogOut, Wifi, WifiOff } from "lucide-react";
import { useWebSocket } from "@/hooks/use-websocket";
import { useState } from "react";
import { formatAge } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme-toggle";
import { useNavigate } from "react-router-dom";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Bot, Plus, Shield } from "lucide-react";

export function Header() {
  const { logout } = useAuth();
  const { connected } = useWebSocket();
  const { data: namespaces } = useNamespaces();
  const { data: canary } = useCanaryConfig();
  const { data: runtimes } = useRuntimes();
  const installDefaultRuntimes = useInstallDefaultRuntimes();
  const [ns, setNs] = useState(getNamespace());
  const [createOpen, setCreateOpen] = useState(false);
  const [choosingHarness, setChoosingHarness] = useState(false);
  const navigate = useNavigate();

  const handleNsChange = (value: string) => {
    setNs(value);
    setNamespace(value);
    window.location.reload();
  };

  return (
    <header className="flex h-14 items-center justify-between border-b border-border/50 bg-card px-6">
      <div className="flex items-center gap-4">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <span>Namespace:</span>
          <Select value={ns} onValueChange={handleNsChange}>
            <SelectTrigger className="h-7 w-44 text-xs">
              <SelectValue placeholder="Select namespace…" />
            </SelectTrigger>
            <SelectContent>
              {(namespaces || []).map((name) => (
                <SelectItem key={name} value={name} className="text-xs">
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="flex items-center gap-3">
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus className="mr-1.5 h-4 w-4" /> Create
        </Button>
        {/* Canary health indicator */}
        {canary?.enabled && canary.healthStatus && (
          <div
            title={`System Canary${canary.lastRunTime ? ` — last check: ${formatAge(canary.lastRunTime)}` : ""}`}
            className={`flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium border cursor-default ${
              canary.healthStatus === "healthy"
                ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/20"
                : canary.healthStatus === "degraded"
                  ? "bg-yellow-500/10 text-yellow-400 border-yellow-500/20"
                  : canary.healthStatus === "unhealthy"
                    ? "bg-red-500/10 text-red-400 border-red-500/20"
                    : "bg-muted/50 text-muted-foreground border-border/50"
            }`}
          >
            <Activity className="h-3 w-3" />
            <span>
              {canary.healthStatus.charAt(0).toUpperCase() +
                canary.healthStatus.slice(1)}
            </span>
          </div>
        )}
        {/* Connection status indicator */}
        <div
          className={`flex items-center gap-2 rounded-full px-3 py-1 text-xs font-medium border ${
            connected
              ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/20"
              : "bg-red-500/10 text-red-400 border-red-500/20"
          }`}
        >
          {connected ? (
            <>
              <span className="relative flex h-2 w-2">
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
              </span>
              <Wifi className="h-3.5 w-3.5" />
              <span>Stream Connected</span>
            </>
          ) : (
            <>
              <span className="relative flex h-2 w-2">
                <span className="relative inline-flex h-2 w-2 rounded-full bg-red-400" />
              </span>
              <WifiOff className="h-3.5 w-3.5" />
              <span>Offline</span>
            </>
          )}
        </div>
        <ThemeToggle />
        <Button variant="ghost" size="icon" onClick={logout} title="Logout">
          <LogOut className="h-4 w-4" />
        </Button>
      </div>
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{choosingHarness ? "Choose a Harness" : "Create"}</DialogTitle>
            <DialogDescription>{choosingHarness ? "Choose an approved runtime. Session-capable harnesses start a persistent session; one-shot harnesses configure a new Agent." : <>Choose what you want to create in namespace <span className="font-mono">{ns}</span>.</>}</DialogDescription>
          </DialogHeader>
          {choosingHarness ? (
            <div className="space-y-3">
              {(runtimes || []).length === 0 ? (
                <div className="space-y-3 rounded-lg border border-border p-4">
                  <p className="text-sm">No approved harnesses are installed in this namespace.</p>
                  <p className="text-xs text-muted-foreground">Install the curated Pi and Hermes runtimes plus their approving policy. This creates persistent namespace resources and never accepts an arbitrary image.</p>
                  <Button size="sm" onClick={() => installDefaultRuntimes.mutate()} disabled={installDefaultRuntimes.isPending}>{installDefaultRuntimes.isPending ? "Installing…" : "Install default harnesses"}</Button>
                </div>
              ) : (
                <div className="space-y-2">
                  {(runtimes || []).map((runtime) => {
                    const persistent = runtime.spec.contractVersion === "v1alpha2" && runtime.spec.session?.protocol === "openai-chat";
                    return <button key={runtime.metadata.name} className="w-full rounded-lg border border-border p-3 text-left transition-colors hover:border-amber-500/60 hover:bg-amber-500/5" onClick={() => {
                      setCreateOpen(false);
                      setChoosingHarness(false);
                      navigate(persistent
                        ? `/harnesses?start=${encodeURIComponent(runtime.metadata.name)}`
                        : `/agents?create=1&runtime=${encodeURIComponent(runtime.metadata.name)}&policy=harness-examples`);
                    }}>
                      <div className="flex items-center gap-2"><Shield className="h-4 w-4 text-amber-500" /><span className="font-medium">{runtime.metadata.name}</span></div>
                      <p className="mt-1 text-xs text-muted-foreground">{persistent ? "Start a persistent session for an existing Agent." : "Create an Agent that uses this harness for one-shot runs."}</p>
                    </button>;
                  })}
                </div>
              )}
              <Button variant="ghost" size="sm" onClick={() => setChoosingHarness(false)}>Back</Button>
            </div>
          ) : <>
          <div className="grid gap-3 sm:grid-cols-2">
            <button className="rounded-lg border border-border p-4 text-left transition-colors hover:border-primary/60 hover:bg-primary/5" onClick={() => { setCreateOpen(false); navigate("/agents?create=1"); }}>
              <Bot className="mb-3 h-5 w-5 text-primary" />
              <p className="font-medium">Agent</p>
              <p className="mt-1 text-xs text-muted-foreground">Create an Agent using Sympozium’s built-in runner.</p>
            </button>
            <button className="rounded-lg border border-border p-4 text-left transition-colors hover:border-primary/60 hover:bg-primary/5" onClick={() => setChoosingHarness(true)}>
              <Shield className="mb-3 h-5 w-5 text-amber-500" />
              <p className="font-medium">Harness</p>
              <p className="mt-1 text-xs text-muted-foreground">Choose a trusted external runtime for one-shot or persistent execution.</p>
            </button>
          </div>
          </>}
        </DialogContent>
      </Dialog>
    </header>
  );
}
