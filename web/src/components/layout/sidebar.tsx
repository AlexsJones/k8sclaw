import { NavLink } from "react-router-dom";
import {
  LayoutDashboard,
  Server,
  Play,
  Shield,
  Wrench,
  Clock,
  Users,
  Github,
  Heart,
  Globe,
  Cpu,
  Plug,
  BookOpen,
  PanelLeftClose,
  PanelLeftOpen,
  Settings,
  Network,
  Activity,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  OntologyModal,
  OntologyModalExpanded,
} from "@/components/ontology-modal";
import { useRuns } from "@/hooks/use-api";
import { useRunsSeen } from "@/hooks/use-runs-seen";
import { useThemeAssets } from "@/hooks/use-theme-assets";

type NavItem = {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  indent?: number;
  badgeKey?: string;
};
type NavSection = { label?: string; items: NavItem[] };

const navSections: NavSection[] = [
  {
    items: [
      { to: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
      { to: "/topology", label: "Topology", icon: Network },
    ],
  },
  {
    label: "Models",
    items: [
      { to: "/models", label: "Models", icon: Cpu },
      { to: "/model-density", label: "Placement & Density", icon: Activity, indent: 1 },
    ],
  },
  {
    label: "Agents",
    items: [
      { to: "/agents", label: "Agents", icon: Server },
      { to: "/ensembles", label: "Ensembles", icon: Users, indent: 1 },
      { to: "/harnesses", label: "Harnesses", icon: Shield, indent: 1 },
      { to: "/runs", label: "Runs", icon: Play, indent: 1, badgeKey: "runs" },
      { to: "/schedules", label: "Schedules", icon: Clock, indent: 1 },
    ],
  },
  {
    label: "Infrastructure",
    items: [
      { to: "/gateway", label: "Gateway", icon: Globe },
      { to: "/policies", label: "Policies", icon: Shield },
      { to: "/skills", label: "Skills", icon: Wrench },
      { to: "/mcp-servers", label: "MCP Servers", icon: Plug },
      { to: "/settings", label: "Settings", icon: Settings },
    ],
  },
];

interface AppSidebarProps {
  collapsed: boolean;
  onToggle: () => void;
}

export function AppSidebar({ collapsed, onToggle }: AppSidebarProps) {
  const { data: runs } = useRuns();
  const { unseenCount } = useRunsSeen();
  const { icon, logo } = useThemeAssets();
  const allRuns = runs || [];
  const unseen = unseenCount(allRuns);
  const unseenFailed = unseenCount(
    allRuns.filter((r) => r.status?.phase === "Failed"),
  );

  const badges: Record<string, { count: number; color: string } | null> = {
    runs:
      unseenFailed > 0
        ? { count: unseenFailed, color: "bg-red-500" }
        : unseen > 0
          ? { count: unseen, color: "bg-blue-500" }
          : null,
  };

  return (
    <aside
      className={cn(
        "flex h-full flex-col border-r border-border/50 bg-card transition-[width] duration-200 ease-in-out",
        collapsed ? "w-14" : "w-60",
      )}
    >
      {/* Logo */}
      <div
        className={cn(
          "flex h-14 items-center border-b border-border/50 overflow-hidden",
          collapsed ? "justify-center px-0" : "px-1.5",
        )}
      >
        {collapsed ? (
          <img src={icon} alt="Sympozium" className="h-6 w-6 shrink-0" />
        ) : (
          <img src={logo} alt="Sympozium" className="w-full" />
        )}
      </div>

      {/* Navigation */}
      <ScrollArea className="flex-1 py-2">
        <nav className="flex flex-col gap-1 px-2">
          {navSections.map((section, si) => (
            <div key={si} className={si > 0 ? "mt-3" : undefined}>
              {section.label && !collapsed && (
                <div className="px-3 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/50">
                  {section.label}
                </div>
              )}
              {section.label && collapsed && (
                <div className="mx-2 mb-1 mt-1 border-t border-border/30" />
              )}
              {section.items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  title={collapsed ? item.label : undefined}
                  className={({ isActive }) =>
                    cn(
                      "relative flex items-center text-sm font-medium transition-colors",
                      collapsed
                        ? "justify-center px-0 py-2"
                        : "gap-3 py-2 pr-3",
                      !collapsed &&
                        (item.indent === 2
                          ? "pl-9"
                          : item.indent === 1
                            ? "pl-6"
                            : "pl-3"),
                      isActive
                        ? "bg-primary/10 text-primary border border-primary/20"
                        : "text-muted-foreground hover:bg-white/5 hover:text-foreground border border-transparent",
                    )
                  }
                >
                  <item.icon className="h-4 w-4 shrink-0" />
                  {!collapsed && item.label}
                  {item.badgeKey && badges[item.badgeKey] && (
                    <span
                      className={cn(
                        "ml-auto inline-flex items-center justify-center text-[10px] font-bold text-white",
                        collapsed
                          ? "absolute -top-1 -right-1 h-4 min-w-4 px-1"
                          : "h-5 min-w-5 px-1.5",
                        badges[item.badgeKey]!.color,
                      )}
                    >
                      {badges[item.badgeKey]!.count}
                    </span>
                  )}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
      </ScrollArea>

      {/* Help & Contribute */}
      {collapsed && (
        <div className="border-t border-border/50 px-2 py-2 flex flex-col items-center gap-1">
          <OntologyModal />
          <a
            href="https://deploy.sympozium.ai/docs"
            target="_blank"
            rel="noopener noreferrer"
            title="Documentation"
            className="flex items-center justify-center p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <BookOpen className="h-4 w-4" />
          </a>
          <a
            href="https://github.com/sympozium-ai/sympozium"
            target="_blank"
            rel="noopener noreferrer"
            title="GitHub"
            className="flex items-center justify-center p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <Github className="h-4 w-4" />
          </a>
        </div>
      )}
      {!collapsed && (
        <div className="border-t border-border/50 px-4 py-3 space-y-2">
          <OntologyModalExpanded />
          <a
            href="https://deploy.sympozium.ai/docs"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <BookOpen className="h-3.5 w-3.5" />
            Documentation
          </a>
          <a
            href="https://github.com/sympozium-ai/sympozium"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <Github className="h-3.5 w-3.5" />
            Star on GitHub
          </a>
          <a
            href="https://github.com/sympozium-ai/sympozium/blob/main/CONTRIBUTING.md"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
          >
            <Heart className="h-3.5 w-3.5" />
            Contribute
          </a>
          <p className="px-2 text-[10px] text-muted-foreground/60">
            Kubernetes-native AI agents
          </p>
        </div>
      )}

      {/* Collapse toggle */}
      <div
        className={cn(
          "border-t border-border/50 py-2",
          collapsed ? "px-2" : "px-4",
        )}
      >
        <button
          onClick={onToggle}
          className={cn(
            "flex items-center py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors w-full",
            collapsed ? "justify-center px-0" : "gap-2 px-2",
          )}
          title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
        >
          {collapsed ? (
            <PanelLeftOpen className="h-4 w-4" />
          ) : (
            <>
              <PanelLeftClose className="h-4 w-4" />
              Collapse
            </>
          )}
        </button>
      </div>
    </aside>
  );
}
