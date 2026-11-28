import { afterEach, expect, it } from "vitest";
import { initI18n } from "../../i18n";
import { repositoryAssistantPrompt } from "./repository-assistant-prompt";

const url = "https://code.example.test/Team/Repo";
const details = { url, name: "", aliases: "", purpose: "", task_workflow: "" } as const;

afterEach(async () => { await initI18n("zh-Hans"); });

it.each([
  { lang: "en", binding: "prepare to bind this repository", source: "ask which host I want to connect", host: "Host: code.example.test" },
  // i18n-ignore: localized prompt fixtures
  { lang: "zh-Hans", binding: "协助绑定此仓库", source: "请先询问我要接入哪个主机", host: "主机：code.example.test" },
  // i18n-ignore: localized prompt fixtures
  { lang: "ja", binding: "このリポジトリのバインドを準備", source: "接続したいホストを私に確認", host: "ホスト：code.example.test" },
])("onboards in $lang without naming CLI tools or a specific platform", async ({ lang, binding, source, host }) => {
  await initI18n(lang);
  const bound = repositoryAssistantPrompt(details);
  const generic = repositoryAssistantPrompt();
  for (const prompt of [bound, generic]) {
    expect(prompt).toContain("repository_providers");
    expect(prompt).toContain("docs/extensions/repository-providers.md");
    expect(prompt).not.toMatch(/\b(?:git|gh|glab|curl|wget|ssh|GitHub|GitLab)\b/i);
  }
  expect(bound).toContain(binding);
  expect(bound).toContain(url);
  expect(bound).toContain(host);
  expect(generic).toContain(source);
  expect(generic).not.toMatch(/https?:\/\/|[\w-]+\.(?:com|net|org|test)\b/);
});

it.each([
  ["HTTPS://account%40example.test:synthetic%40password@CODE.Example.test:8443/Team/%52epo", "HTTPS://CODE.Example.test:8443/Team/%52epo"],
  ["https://code.example.test/Team/Repo?provider_secret=synthetic#synthetic", url],
  ["ssh://account:synthetic@code.example.test:2222/Team/Repo", "ssh://code.example.test:2222/Team/Repo"],
  ["account@code.example.test:Team/Repo", "ssh://code.example.test/Team/Repo"],
  ["https://@code.example.test/Team/Repo", url],
])("keeps the repository address and host after stripping credentials (%#)", async (input, sanitized) => {
  await initI18n("en");
  const prompt = repositoryAssistantPrompt({ ...details, url: input });
  expect(prompt.split("\n")).toContain("Repository URL: " + sanitized);
  expect(prompt.split("\n")).toContain("Host: code.example.test");
  expect(prompt).not.toMatch(/account|synthetic|@/);
});

it("asks for a usable address without quoting invalid input and retains filled details", async () => {
  await initI18n("en");
  const prompt = repositoryAssistantPrompt({
    ...details, url: "https://account:synthetic@code.example.test:bad/Team/Repo",
    name: "   ", aliases: "  ,", purpose: "  Review changes  ", task_workflow: "isolated",
  });
  expect(prompt).toContain("Ask me for the repository URL without credentials.");
  expect(prompt).not.toMatch(/account|synthetic|code\.example|Name:|Aliases:/);
  expect(prompt.split("\n")).toContain("Purpose: Review changes");
  expect(prompt.split("\n")).toContain("Task follow-up: isolated");
});
