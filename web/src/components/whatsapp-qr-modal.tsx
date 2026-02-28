import { useEffect, useMemo, useState } from "react";
import { api, type PodInfo } from "@/lib/api";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Loader2, QrCode } from "lucide-react";

type QRState = "waiting" | "scanning" | "linked" | "error";

interface WhatsAppQRModalProps {
  open: boolean;
  onClose: () => void;
  instanceName?: string;
  personaPackName?: string;
}

function extractQRLines(logs: string): string[] {
  const lines = logs.split("\n");
  const qrLines: string[] = [];
  let inQR = false;
  for (const line of lines) {
    if (line.includes("Scan this QR code")) {
      inQR = true;
      qrLines.push(line);
      continue;
    }
    if (inQR) {
      qrLines.push(line);
      if (line.trim() === "" && qrLines.length > 5) {
        break;
      }
    }
  }
  return qrLines;
}

export function WhatsAppQRModal({
  open,
  onClose,
  instanceName,
  personaPackName,
}: WhatsAppQRModalProps) {
  const [state, setState] = useState<QRState>("waiting");
  const [status, setStatus] = useState("Waiting for WhatsApp channel pod…");
  const [podName, setPodName] = useState("");
  const [qrLines, setQrLines] = useState<string[]>([]);
  const [error, setError] = useState("");

  const targetLabel = useMemo(() => {
    if (instanceName) return `instance ${instanceName}`;
    if (personaPackName) return `persona pack ${personaPackName}`;
    return "target";
  }, [instanceName, personaPackName]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let linked = false;

    const findWhatsAppPod = (pods: PodInfo[]): PodInfo | undefined => {
      return pods.find((p) => {
        const labels = p.labels || {};
        if (labels["sympozium.ai/channel"] !== "whatsapp") return false;
        const inst = labels["sympozium.ai/instance"] || p.instanceRef || "";
        if (instanceName) return inst === instanceName;
        if (personaPackName) return inst.startsWith(`${personaPackName}-`);
        return false;
      });
    };

    const poll = async () => {
      try {
        const pods = await api.pods.list();
        const pod = findWhatsAppPod(pods);
        if (!pod) {
          if (!cancelled) {
            setState("waiting");
            setStatus("Waiting for WhatsApp channel pod to start…");
          }
          return;
        }

        if (!cancelled) {
          setPodName(pod.name);
        }

        if (pod.phase !== "Running") {
          if (!cancelled) {
            setState("waiting");
            setStatus(`Waiting for pod ${pod.name} (${pod.phase})…`);
          }
          return;
        }

        const logRes = await api.pods.logs(pod.name);
        const logs = logRes.logs || "";
        if (logs.includes("linked successfully") || logs.includes("connected with existing session")) {
          if (!cancelled) {
            setState("linked");
            setStatus("WhatsApp linked successfully.");
            setError("");
          }
          linked = true;
          return;
        }

        const lines = extractQRLines(logs);
        if (lines.length > 0) {
          if (!cancelled) {
            setState("scanning");
            setQrLines(lines);
            setStatus("Scan the QR code with WhatsApp.");
            setError("");
          }
          return;
        }

        if (!cancelled) {
          setState("waiting");
          setStatus("WhatsApp channel is initializing…");
        }
      } catch (err) {
        if (!cancelled) {
          setState("error");
          setError(err instanceof Error ? err.message : "Failed to poll WhatsApp status");
          setStatus("Retrying…");
        }
      }
    };

    poll();
    const loop = () => {
      timer = setTimeout(async () => {
        await poll();
        if (!cancelled && !linked) {
          loop();
        }
      }, 3000);
    };
    loop();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      setState("waiting");
      setStatus("Waiting for WhatsApp channel pod…");
      setPodName("");
      setQrLines([]);
      setError("");
    };
  }, [open, instanceName, personaPackName]);

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <QrCode className="h-5 w-5 text-green-400" />
            WhatsApp Pairing
          </DialogTitle>
          <DialogDescription>
            Finalize channel setup for {targetLabel}. Open WhatsApp on your phone:
            Settings → Linked Devices → Link a Device.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {podName && (
            <p className="text-xs text-muted-foreground">
              Pod: <span className="font-mono">{podName}</span>
            </p>
          )}
          {(state === "waiting" || state === "error") && (
            <div className="rounded-md border border-border/50 bg-muted/20 p-3 text-sm">
              <div className="flex items-center gap-2">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span>{status}</span>
              </div>
              {error && (
                <p className="mt-2 text-xs text-red-400">{error}</p>
              )}
            </div>
          )}
          {state === "scanning" && qrLines.length > 0 && (
            <pre className="max-h-72 overflow-auto rounded-md border border-border/50 bg-black/40 p-3 text-[11px] leading-tight text-green-300">
              {qrLines.join("\n")}
            </pre>
          )}
          {state === "linked" && (
            <div className="rounded-md border border-emerald-500/30 bg-emerald-500/10 p-3 text-sm text-emerald-300">
              {status}
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" onClick={onClose}>
            {state === "linked" ? "Done" : "Close"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
