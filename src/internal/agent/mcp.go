package agent

const pythonMCPScript = `
class _MCP:
    def __init__(self):
        self._loop = None
        self._thread = None
        self._clients = {}

    def _config_path(self):
        configured = os.environ.get("FN_MCP_CONFIG")
        if configured:
            return os.path.expanduser(configured)
        return os.path.expanduser("~/.fn/mcp.json")

    def _config(self):
        with open(self._config_path(), encoding="utf-8") as config_file:
            config = json.load(config_file)
        return config.get("mcpServers", config.get("servers", config))

    def servers(self):
        """Return the names of configured MCP servers."""
        return list(self._config())

    def _ensure_loop(self):
        if self._loop is not None:
            return
        import asyncio
        import threading
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(target=self._loop.run_forever, daemon=True)
        self._thread.start()

    def _run(self, coroutine):
        import asyncio
        self._ensure_loop()
        return asyncio.run_coroutine_threadsafe(coroutine, self._loop).result()

    def _expand(self, value):
        if isinstance(value, str):
            return os.path.expandvars(os.path.expanduser(value))
        if isinstance(value, list):
            return [self._expand(item) for item in value]
        if isinstance(value, dict):
            return {key: self._expand(item) for key, item in value.items()}
        return value

    def _oauth(self, config):
        from fastmcp.client.auth import OAuth
        from key_value.aio._utils.sanitization import AlwaysHashStrategy
        from key_value.aio.stores.filetree import FileTreeStore
        from urllib.parse import urlparse

        token_dir = os.path.expanduser("~/.fn/mcp-auth")
        os.makedirs(token_dir, mode=0o700, exist_ok=True)
        os.chmod(token_dir, 0o700)
        redirect = config.get("oauthRedirectUrl")
        callback_port = urlparse(redirect).port if redirect else None
        return OAuth(
            scopes=config.get("oauthScopes"),
            client_name="fn agent",
            client_id=config.get("oauthClientId"),
            client_secret=config.get("oauthClientSecret"),
            callback_port=callback_port,
            token_storage=FileTreeStore(
                data_directory=token_dir,
                key_sanitization_strategy=AlwaysHashStrategy(),
            ),
        )

    def _transport(self, config):
        from fastmcp.client.transports import SSETransport, StdioTransport, StreamableHttpTransport

        config = self._expand(config)
        if "command" in config:
            return StdioTransport(
                command=config["command"],
                args=config.get("args", []),
                env=config.get("env"),
                cwd=config.get("cwd"),
                log_file=sys.__stderr__,
            )

        url = config.get("url", config.get("baseUrl"))
        auth = self._oauth(config) if config.get("auth") == "oauth" else config.get("auth")
        transport = SSETransport if config.get("type") == "sse" else StreamableHttpTransport
        return transport(url=url, headers=config.get("headers"), auth=auth)

    async def _client(self, server):
        if server in self._clients:
            return self._clients[server]
        try:
            from fastmcp import Client
        except ImportError as error:
            raise RuntimeError(
                "MCP requires fastmcp: python3 -m pip install 'fastmcp-slim[client]>=3.4,<4' 'websockets>=15'"
            ) from error
        config = self._config()[server]
        client = Client(self._transport(config))
        await client.__aenter__()
        self._clients[server] = client
        return client

    def _plain(self, value):
        if hasattr(value, "model_dump"):
            return self._plain(value.model_dump(by_alias=True, exclude_none=True))
        if dataclasses.is_dataclass(value):
            return self._plain(dataclasses.asdict(value))
        if isinstance(value, dict):
            return {key: self._plain(item) for key, item in value.items()}
        if isinstance(value, (list, tuple)):
            return [self._plain(item) for item in value]
        return value

    async def _tools(self, server):
        client = await self._client(server)
        return [self._plain(tool) for tool in await client.list_tools()]

    def tools(self, server):
        """Return tool definitions for one MCP server."""
        return self._run(self._tools(server))

    def search(self, query, server=None):
        """Search tool names and descriptions, optionally within one server."""
        words = query.lower().split()
        servers = [server] if server else self.servers()
        matches = []
        for server_name in servers:
            for tool in self.tools(server_name):
                text = (tool.get("name", "") + " " + tool.get("description", "")).lower()
                score = sum(word in text for word in words)
                if score:
                    matches.append((score, server_name, tool))
        matches.sort(key=lambda match: (-match[0], match[1], match[2].get("name", "")))
        return [dict(match[2], server=match[1]) for match in matches]

    def _split_name(self, name):
        for server in self.servers():
            prefix = server + "."
            if name.startswith(prefix):
                return server, name[len(prefix):]
        raise ValueError("MCP tool names must be <server>.<tool>")

    def schema(self, name):
        """Return the definition of an MCP tool named <server>.<tool>."""
        server, tool_name = self._split_name(name)
        for tool in self.tools(server):
            if tool.get("name") == tool_name:
                return tool
        raise KeyError(name)

    async def _call(self, server, tool, arguments):
        client = await self._client(server)
        result = await client.call_tool(tool, arguments)
        if result.is_error:
            text = "\n".join(block.text for block in result.content if hasattr(block, "text"))
            raise RuntimeError(text or "MCP tool call failed")
        if result.data is not None:
            return self._plain(result.data)
        if result.structured_content is not None:
            return self._plain(result.structured_content)
        if len(result.content) == 1 and hasattr(result.content[0], "text"):
            text = result.content[0].text
            try:
                return json.loads(text)
            except json.JSONDecodeError:
                return text
        return self._plain(result.content)

    def call(self, name, arguments=None, **kwargs):
        """Call an MCP tool named <server>.<tool>. Pass arguments as a dict or keywords."""
        server, tool = self._split_name(name)
        values = {} if arguments is None else dict(arguments)
        values.update(kwargs)
        return self._run(self._call(server, tool, values))

    async def _close(self):
        for client in reversed(list(self._clients.values())):
            await client.__aexit__(None, None, None)
        self._clients.clear()

    def close(self):
        """Close all active MCP connections."""
        if self._loop is not None:
            self._run(self._close())

mcp = _MCP()
`
