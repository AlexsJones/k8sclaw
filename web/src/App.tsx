import { Routes, Route, Navigate } from "react-router-dom";
import { useAuth } from "@/components/auth-provider";
import { Layout } from "@/components/layout/layout";
import { LoginPage } from "@/pages/login";
import { DashboardPage } from "@/pages/dashboard";
import { AgentsPage } from "@/pages/agents";
import { AgentDetailPage } from "@/pages/agent-detail";
import { RunsPage } from "@/pages/runs";
import { RunDetailPage } from "@/pages/run-detail";
import { PoliciesPage } from "@/pages/policies";
import { SkillsPage } from "@/pages/skills";
import { SchedulesPage } from "@/pages/schedules";
import { EnsemblesPage } from "@/pages/ensembles";
import { EnsembleDetailPage } from "@/pages/ensemble-detail";
import { EnsembleBuilderPage } from "@/pages/ensemble-builder";
import { GatewayPage } from "@/pages/gateway";
import { McpServersPage } from "@/pages/mcp-servers";
import { McpServerDetailPage } from "@/pages/mcp-server-detail";
import { ModelsPage } from "@/pages/models";
import { ModelDetailPage } from "@/pages/model-detail";
import { SettingsPage } from "@/pages/settings";
import { TopologyPage } from "@/pages/topology";
import { TopologyDemoPage } from "@/pages/topology-demo";
import { SyntheticMembranePage } from "@/pages/synthetic-membrane";
import { ModelDensityPage } from "@/pages/model-density";
import { HarnessesPage } from "@/pages/harnesses";
import { HarnessDetailPage } from "@/pages/harness-detail";

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuth();
  if (!isAuthenticated) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  const { isAuthenticated } = useAuth();

  if (!isAuthenticated) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route
        element={
          <ProtectedRoute>
            <Layout />
          </ProtectedRoute>
        }
      >
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/topology" element={<TopologyPage />} />
        <Route path="/topology/demo" element={<TopologyDemoPage />} />
        <Route path="/agents" element={<AgentsPage />} />
        <Route path="/agents/:name" element={<AgentDetailPage />} />
        <Route path="/harnesses" element={<HarnessesPage />} />
        <Route path="/harnesses/:name" element={<HarnessDetailPage />} />
        <Route path="/runs" element={<RunsPage />} />
        <Route path="/runs/:name" element={<RunDetailPage />} />
        <Route path="/policies" element={<PoliciesPage />} />
        <Route path="/skills" element={<SkillsPage />} />
        <Route path="/schedules" element={<SchedulesPage />} />
        <Route path="/ensembles" element={<EnsemblesPage />} />
        <Route path="/ensembles/new" element={<EnsembleBuilderPage />} />
        <Route path="/ensembles/:name" element={<EnsembleDetailPage />} />
        <Route path="/mcp-servers" element={<McpServersPage />} />
        <Route path="/mcp-servers/:name" element={<McpServerDetailPage />} />
        <Route path="/model-density" element={<ModelDensityPage />} />
        <Route path="/models" element={<ModelsPage />} />
        <Route path="/models/:name" element={<ModelDetailPage />} />
        <Route path="/gateway" element={<GatewayPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="/synthetic-membrane" element={<SyntheticMembranePage />} />
      </Route>
      <Route path="/login" element={<LoginPage />} />
      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
