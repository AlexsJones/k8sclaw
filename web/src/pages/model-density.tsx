import { useState } from "react";
import { useDensityNodes, useDensityQuery, useDraNodes, useModelCatalog } from "@/hooks/use-api";
import { AcceleratorLeaves } from "@/components/accelerator-leaves";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Cpu, HardDrive, Activity, Search } from "lucide-react";

function fitLevelColor(level: string) {
  switch (level) {
    case "perfect":
      return "bg-green-500/15 text-green-700 border-green-500/30";
    case "good":
      return "bg-yellow-500/15 text-yellow-700 border-yellow-500/30";
    case "marginal":
      return "bg-orange-500/15 text-orange-700 border-orange-500/30";
    case "too_tight":
      return "bg-red-500/15 text-red-700 border-red-500/30";
    default:
      return "";
  }
}

function FitBadge({ level }: { level: string }) {
  return (
    <Badge variant="outline" className={fitLevelColor(level)}>
      {level}
    </Badge>
  );
}

function formatRAM(gb: number) {
  return `${Math.round(gb)} GB`;
}

export function ModelDensityPage() {
  const [modelQuery, setModelQuery] = useState("");
  const { data: nodesData, isLoading: nodesLoading } = useDensityNodes();
  const { data: draData } = useDraNodes();
  const { data: catalogData, isLoading: catalogLoading } = useModelCatalog();
  const { data: queryData } = useDensityQuery(modelQuery);

  const nodes = nodesData?.nodes || [];
  const draByNode = new Map(
    (draData?.nodes || []).map((n) => [n.nodeName, n.devices]),
  );
  const densityNames = new Set(nodes.map((n) => n.nodeName));
  const draOnlyNodes = (draData?.nodes || []).filter(
    (n) => !densityNames.has(n.nodeName),
  );
  const catalog = catalogData?.models || [];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Model Density</h1>
        <p className="text-muted-foreground">
          Real-time model density data from the llmfit DaemonSet
        </p>
      </div>

      {nodesLoading ? (
        <div className="grid gap-4 md:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-32" />
          ))}
        </div>
      ) : nodes.length === 0 && draOnlyNodes.length === 0 ? (
        <Card>
          <CardContent className="py-10 text-center text-muted-foreground">
            <Activity className="mx-auto h-10 w-10 mb-3 opacity-50" />
            <p>No density data available.</p>
            <p className="text-sm mt-1">
              Deploy the llmfit DaemonSet to enable model density monitoring.
            </p>
          </CardContent>
        </Card>
      ) : (
        <Tabs defaultValue="nodes">
          <TabsList>
            <TabsTrigger value="nodes">Nodes</TabsTrigger>
            <TabsTrigger value="catalog">Model Catalog</TabsTrigger>
            <TabsTrigger value="query">Query</TabsTrigger>
          </TabsList>

          <TabsContent value="nodes" className="space-y-4">
            <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
              {nodes.map((node) => (
                <Card key={node.nodeName}>
                  <CardHeader className="pb-2">
                    <CardTitle className="text-sm font-medium flex items-center justify-between">
                      <span className="font-mono">{node.nodeName}</span>
                      {node.stale && (
                        <Badge variant="destructive" className="text-xs">
                          stale
                        </Badge>
                      )}
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="space-y-2 text-sm">
                    <div className="flex items-center gap-2">
                      <Cpu className="h-3.5 w-3.5 text-muted-foreground" />
                      <span>{node.system.cpu_name || `${node.system.cpu_cores} cores`}</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <HardDrive className="h-3.5 w-3.5 text-muted-foreground" />
                      <span>
                        {formatRAM(node.system.total_ram_gb)} RAM
                        {node.system.has_gpu && node.system.gpu_name && (
                          <> &middot; {node.system.gpu_name}</>
                        )}
                        {node.system.has_gpu && node.system.gpu_vram_gb && (
                          <> ({formatRAM(node.system.gpu_vram_gb)} VRAM)</>
                        )}
                      </span>
                    </div>
                    <div className="flex items-center gap-2">
                      <Activity className="h-3.5 w-3.5 text-muted-foreground" />
                      <span>{node.modelFitCount} models fit</span>
                    </div>
                    {node.runtimes && node.runtimes.length > 0 && (
                      <div className="flex flex-wrap gap-1 pt-1">
                        {node.runtimes
                          .filter((r) => r.installed)
                          .map((r) => (
                            <Badge
                              key={r.name}
                              variant="secondary"
                              className="text-xs"
                            >
                              {r.name}
                            </Badge>
                          ))}
                      </div>
                    )}
                    <AcceleratorLeaves devices={draByNode.get(node.nodeName) || []} node={node.nodeName} />
                  </CardContent>
                </Card>
              ))}
              {draOnlyNodes.map((n) => (
                <Card key={n.nodeName}>
                  <CardHeader className="pb-2">
                    <CardTitle className="text-sm font-medium flex items-center justify-between">
                      <span className="font-mono">{n.nodeName}</span>
                      <Badge variant="outline" className="text-xs">
                        dra
                      </Badge>
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="space-y-2 text-sm">
                    <AcceleratorLeaves devices={n.devices} node={n.nodeName} />
                  </CardContent>
                </Card>
              ))}
            </div>
          </TabsContent>

          <TabsContent value="catalog" className="space-y-4">
            {catalogLoading ? (
              <Skeleton className="h-64" />
            ) : catalog.length === 0 ? (
              <p className="text-muted-foreground text-sm py-4">
                No model fitness data available yet.
              </p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Model</TableHead>
                    <TableHead>Best Score</TableHead>
                    <TableHead>Fit Level</TableHead>
                    <TableHead>Best Node</TableHead>
                    <TableHead>Available On</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {catalog.slice(0, 50).map((entry) => (
                    <TableRow key={entry.modelName}>
                      <TableCell className="font-mono text-sm">
                        {entry.modelName}
                      </TableCell>
                      <TableCell>{Math.round(entry.bestScore)}</TableCell>
                      <TableCell>
                        <FitBadge level={entry.fitLevel} />
                      </TableCell>
                      <TableCell className="font-mono text-sm">
                        {entry.bestNode}
                      </TableCell>
                      <TableCell>
                        {entry.nodes.length} node{entry.nodes.length !== 1 && "s"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </TabsContent>

          <TabsContent value="query" className="space-y-4">
            <div className="flex items-center gap-2 max-w-md">
              <Search className="h-4 w-4 text-muted-foreground" />
              <Input
                placeholder="Search models (e.g., Qwen2.5, Llama)"
                value={modelQuery}
                onChange={(e) => setModelQuery(e.target.value)}
              />
            </div>
            {modelQuery && queryData?.rankedNodes && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Node</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead>Score</TableHead>
                    <TableHead>Fit Level</TableHead>
                    <TableHead>Est. TPS</TableHead>
                    <TableHead>Memory</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {queryData.rankedNodes.map((result, i) => (
                    <TableRow key={`${result.nodeName}-${i}`}>
                      <TableCell className="font-mono text-sm">
                        {result.nodeName}
                      </TableCell>
                      <TableCell className="font-mono text-sm">
                        {result.model.name}
                      </TableCell>
                      <TableCell>{Math.round(result.score)}</TableCell>
                      <TableCell>
                        <FitBadge level={result.fitLevel} />
                      </TableCell>
                      <TableCell>
                        {result.model.estimated_tps.toFixed(1)} tok/s
                      </TableCell>
                      <TableCell>
                        {result.model.memory_required_gb.toFixed(1)} /{" "}
                        {result.model.memory_available_gb.toFixed(1)} GB
                      </TableCell>
                    </TableRow>
                  ))}
                  {queryData.rankedNodes.length === 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={6}
                        className="text-center text-muted-foreground py-4"
                      >
                        No matching models found for "{modelQuery}"
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            )}
          </TabsContent>
        </Tabs>
      )}
    </div>
  );
}
