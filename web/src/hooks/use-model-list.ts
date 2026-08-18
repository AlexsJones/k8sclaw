import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

// ── Static fallback model lists ──────────────────────────────────────────────

const OPENAI_MODELS = [
  "gpt-4o",
  "gpt-4o-mini",
  "gpt-4-turbo",
  "gpt-4",
  "gpt-3.5-turbo",
  "o1",
  "o1-mini",
  "o3-mini",
];

const ANTHROPIC_MODELS = [
  "claude-sonnet-4-20250514",
  "claude-3-5-haiku-20241022",
  "claude-3-opus-20240229",
  "claude-3-5-sonnet-20241022",
];

const AZURE_MODELS = ["gpt-4o", "gpt-4", "gpt-35-turbo"];

const BEDROCK_MODELS = [
  "anthropic.claude-sonnet-4-20250514-v1:0",
  "anthropic.claude-haiku-4-5-20251001-v1:0",
  "amazon.nova-pro-v1:0",
  "amazon.nova-lite-v1:0",
];

// ── Fetchers ─────────────────────────────────────────────────────────────────

async function fetchOpenAIModels(apiKey: string): Promise<string[]> {
  const res = await fetch("https://api.openai.com/v1/models", {
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  if (!res.ok) throw new Error("Failed to fetch OpenAI models");
  const data = await res.json();
  return (data.data as { id: string }[])
    .map((m) => m.id)
    .filter(
      (id) =>
        id.startsWith("gpt-") ||
        id.startsWith("o1") ||
        id.startsWith("o3") ||
        id.startsWith("o4"),
    )
    .sort((a, b) => a.localeCompare(b));
}

async function fetchAnthropicModels(apiKey: string): Promise<string[]> {
  const res = await fetch("https://api.anthropic.com/v1/models", {
    headers: {
      "x-api-key": apiKey,
      "anthropic-version": "2023-06-01",
      "anthropic-dangerous-direct-browser-access": "true",
    },
  });
  if (!res.ok) throw new Error("Failed to fetch Anthropic models");
  const data = await res.json();
  return (data.data as { id: string }[]).map((m) => m.id).sort();
}

async function fetchProviderModelsDirect(baseURL: string): Promise<string[]> {
  // Try fetching directly from the browser (works when the provider is on the
  // local network / same machine and CORS allows it or isn't enforced).
  const modelsURL = baseURL.replace(/\/+$/, "") + "/models";
  const res = await fetch(modelsURL, { signal: AbortSignal.timeout(3000) });
  if (!res.ok) throw new Error(`Direct fetch failed: ${res.status}`);
  const data = await res.json();
  // OpenAI-compatible format: { data: [{ id: "model-name" }, ...] }
  if (Array.isArray(data?.data)) {
    return (data.data as { id: string }[]).map((m) => m.id).sort();
  }
  // llama-server may return { models: [...] } or similar
  if (Array.isArray(data?.models)) {
    return (data.models as { id?: string; name?: string }[])
      .map((m) => m.id || m.name || "")
      .filter(Boolean)
      .sort();
  }
  throw new Error("Unrecognized model list format");
}

async function fetchProviderModelsViaProxy(
  baseURL: string,
  apiKey?: string,
  provider?: string,
  secretName?: string,
): Promise<string[]> {
  const res = await api.providers.models(
    baseURL,
    apiKey,
    provider,
    secretName,
  );
  return res.models;
}

async function fetchLocalProviderModels(
  baseURL: string,
  apiKey?: string,
): Promise<string[]> {
  // Try direct browser fetch first (works for LAN / localhost providers).
  // Falls back to in-cluster proxy if direct fails (CORS, network, etc.).
  try {
    return await fetchProviderModelsDirect(baseURL);
  } catch {
    return fetchProviderModelsViaProxy(baseURL, apiKey);
  }
}

async function fetchBedrockModels(
  creds: BedrockCredentials,
): Promise<string[]> {
  const res = await api.providers.bedrockModels({
    region: creds.region,
    accessKeyId: creds.accessKeyId,
    secretAccessKey: creds.secretAccessKey,
    sessionToken: creds.sessionToken || undefined,
  });
  return res.models;
}

// ── Types ────────────────────────────────────────────────────────────────────

export interface BedrockCredentials {
  region: string;
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken?: string;
}

// ── Hook ─────────────────────────────────────────────────────────────────────

/**
 * Fetches the model list for a given provider + API key.
 * For local providers (ollama, custom) with a baseURL, proxies through the backend.
 * For Bedrock, proxies through the backend with AWS credentials.
 * Falls back to a curated static list if the API call fails or no key is given.
 */
export function useModelList(
  provider: string,
  apiKey: string,
  baseURL?: string,
  bedrockCredentials?: BedrockCredentials,
  secretName?: string,
) {
  const isLocalProvider =
    provider === "ollama" ||
    provider === "lm-studio" ||
    provider === "llama-server" ||
    provider === "unsloth" ||
    provider === "custom";
  const canFetchLocal = isLocalProvider && !!baseURL;
  const canFetchCloud =
    !!apiKey && (provider === "openai" || provider === "anthropic");
  const canFetchBedrock =
    provider === "bedrock" &&
    !!bedrockCredentials?.region &&
    !!bedrockCredentials?.accessKeyId &&
    !!bedrockCredentials?.secretAccessKey;
  const canFetchDatabricks =
    provider === "databricks" && !!baseURL && (!!apiKey || !!secretName);

  const query = useQuery<string[]>({
    queryKey: [
      "provider-models",
      provider,
      apiKey,
      baseURL,
      bedrockCredentials?.region,
      bedrockCredentials?.accessKeyId,
      secretName,
    ],
    queryFn: async () => {
      if (canFetchDatabricks)
        return fetchProviderModelsViaProxy(
          baseURL!,
          apiKey || undefined,
          provider,
          secretName,
        );
      if (canFetchLocal)
        return fetchLocalProviderModels(baseURL!, apiKey || undefined);
      if (canFetchBedrock) return fetchBedrockModels(bedrockCredentials!);
      if (provider === "openai" && apiKey) return fetchOpenAIModels(apiKey);
      if (provider === "anthropic" && apiKey)
        return fetchAnthropicModels(apiKey);
      throw new Error("no-fetch");
    },
    enabled:
      canFetchLocal || canFetchCloud || canFetchBedrock || canFetchDatabricks,
    staleTime: 5 * 60 * 1000, // cache 5 min
    retry: false,
  });

  // Static fallback when fetch isn't available or failed
  const fallback = (() => {
    switch (provider) {
      case "openai":
        return OPENAI_MODELS;
      case "anthropic":
        return ANTHROPIC_MODELS;
      case "azure-openai":
        return AZURE_MODELS;
      case "bedrock":
        return BEDROCK_MODELS;
      default:
        return [];
    }
  })();

  return {
    models: query.data ?? fallback,
    isLoading: query.isLoading && query.fetchStatus !== "idle",
    isLive: !!query.data, // true if we got real data from the API
  };
}
