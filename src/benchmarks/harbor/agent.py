import json
import shlex
from pathlib import Path
from typing import override

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.agents.model_connection import ModelConnectionSpec
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext


class FnAgent(BaseInstalledAgent):
    MODEL_CONNECTION = ModelConnectionSpec(
        default_provider="openai",
        api_key_envs=("OPENAI_API_KEY",),
        base_url_envs=("FN_BASE_URL", "OPENAI_BASE_URL"),
    )

    def __init__(
        self, *args, reasoning_effort: str = "medium", local_binary: str | None = None, **kwargs
    ):
        super().__init__(*args, **kwargs)
        self.reasoning_effort = reasoning_effort
        self.local_binary = local_binary

    @staticmethod
    @override
    def name() -> str:
        return "fn"

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        if self.local_binary:
            await self.exec_as_root(environment, command="mkdir -p /installed-agent/bin")
            await environment.upload_file(Path(self.local_binary), "/installed-agent/bin/fn")
            await self.exec_as_root(
                environment,
                command="chmod 755 /installed-agent/bin/fn",
            )
            return

        await self.ensure_system_dependencies(
            environment,
            ("curl", "ca_certificates", "python3", "tar"),
        )
        version = self._version or "master"
        source_url = f"https://github.com/Baitinq/fn-agent/archive/{version}.tar.gz"
        await self.exec_as_root(
            environment,
            command=(
                "set -e; "
                'case "$(uname -m)" in '
                "x86_64) arch=amd64 ;; "
                "aarch64) arch=arm64 ;; "
                "esac; "
                "mkdir -p /installed-agent/fn /installed-agent/bin; "
                "curl -fsSL "
                '"https://go.dev/dl/go1.26.0.linux-${arch}.tar.gz" '
                "| tar -xz -C /installed-agent; "
                f"curl -fsSL {shlex.quote(source_url)} "
                "| tar -xz --strip-components=1 -C /installed-agent/fn; "
                "cd /installed-agent/fn/src; "
                "/installed-agent/go/bin/go build "
                "-o /installed-agent/bin/fn ./cmd/fn"
            ),
        )

    @override
    @with_prompt_template
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        model = (self.model_name or "openai/gpt-5.6-sol").split("/", 1)[-1]
        env = dict(self.model_connection.env)
        env.update(
            {
                "FN_MODEL": model,
                "FN_REASONING_EFFORT": self.reasoning_effort,
                "HOME": "/logs/agent/home",
            }
        )
        if self.model_connection.configured_base_url:
            env["FN_BASE_URL"] = self.model_connection.configured_base_url

        await self.exec_as_agent(
            environment,
            command=(
                "mkdir -p /logs/agent/home; "
                f"printf %s {shlex.quote(instruction)} "
                "| /installed-agent/bin/fn -p --json "
                "2> /logs/agent/fn.stderr "
                "| tee /logs/agent/fn.jsonl"
            ),
            env=env,
        )

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        output_file = self.logs_dir / "fn.jsonl"
        if not output_file.exists():
            return

        input_tokens = 0
        cached_tokens = 0
        output_tokens = 0
        for line in output_file.read_text().splitlines():
            event = json.loads(line)
            if event.get("type") != "response":
                continue
            usage = event["usage"]
            input_tokens += usage["input_tokens"]
            cached_tokens += usage["cached_input_tokens"]
            output_tokens += usage["output_tokens"]

        context.n_input_tokens = input_tokens
        context.n_cache_tokens = cached_tokens
        context.n_output_tokens = output_tokens
