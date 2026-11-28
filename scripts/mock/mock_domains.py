"""The mock hub's domains: one made-up area of the hub each, in a module of its own (scripts/mock/mock_hub.py).

mock_hub.py is one long file that nearly every mock change edits. A domain module keeps its area out of it: it
registers its HTTP routes, WebSocket messages, hello features, MOCK_* help lines and /mock/reset hooks here, and
mock_hub.py consults the registry at its few central points. Adding a domain is a new module plus one line in DOMAINS
below; mock_hub.py does not change (docs/operations/testing.md "Mock hub domains").

    # scripts/mock/example_mock.py
    import os

    MODE = os.environ.get("MOCK_EXAMPLE", "")


    def register(d):
        d.help("MOCK_EXAMPLE=1  what the switch does (#<issue>)")
        if not MODE:
            return  # off: the hub answers as it did; the help line stays
        pings = {"n": 0}
        d.feature("example")                                         # listed in the hello and /api/v1/health
        d.drop_feature("machine_rename", when=lambda: MODE == "old")  # an older hub
        d.route("GET", "/mock/example", lambda h, req: h.send_json(pings))
        d.route("POST", r"/api/v1/example/(?P<id>[^/]+)", save)     # save(h, req): req.match["id"], req.json()
        d.ws("example_ping", ping)                                   # ping(ws, msg): ws.send({...})
        d.on_ws_close(forget)                                        # forget(ws)
        d.on_reset(lambda: pings.update(n=0))                        # GET /mock/reset

(save, ping and forget are the module's own functions.) scripts/mock/desktops_mock.py is a real one.

Order: domains load in DOMAINS order after every built-in switch has built its fixtures (so a domain can read and
extend them) and before the To-dos view (todo_view_mock.py), which replaces the to-dos. A request reaches the
registered routes after the token check (and a GET's hit count) and before the route chains of H.do_GET and
H.do_POST; a route outside /mock/ first gets the simulated network faults the built-in /api routes get (/mock/down
closes the connection unanswered, /mock/delay waits; /mock/reject and the token checks come before it, and a lost
receipt reaches it through h.send_json). A WebSocket message reaches the registered handlers before the built-in ones.
A handler that returns False hands either on to the next one and then to mock_hub.py. Close handlers run each on its
own: one that raises is logged and the rest, and mock_hub.py's own cleanup, still run. /mock/reset hooks run after the
built-in reset, so a hook can put back what the reset rebuilt. Registered features apply after the built-in ones, so a
domain can also drop one; a block that moves out of mock_hub.py into a domain can so move its features later in the
list (MOCK_DESKTOPS=1 with MOCK_AGENT_ARCHIVES=1: desktops, desktop_unregister now follow the agent archive features),
and its help lines after the built-in ones. Clients read the features as a set.

Moving a block out of mock_hub.py: list the names it took in MOVED in scripts/mock/test_mock_domains.py. A branch from
before the move that edited the old block can resolve its conflict by keeping it (#11476), and the domain would then
exist twice; that test fails as soon as one of those names is back in mock_hub.py, and says which module to edit.
"""
import importlib
import json
import re
import sys
import traceback
from urllib.parse import parse_qs, urlparse

# The domains, loaded in this order: an earlier domain's route wins, and features and help lines follow it.
DOMAINS = [
    "health_tell_mock",  # Tell Health acceptance and duplicate receipts (#14715)
    "health_intake_mock",  # Health intake records, pages and water migration (#13176, #13636)
    "desktops_mock",  # desktop client registration (hub/desktops.go), GET /mock/desktops, MOCK_DESKTOPS
    "bg_stalled_mock",  # an Agent whose background work stalled, still Working (#13744), MOCK_BG_STALLED
    "side_unread_mock",  # side sessions with unseen replies for the sidebar's Unread row (#13496), MOCK_SIDE_UNREAD
    "herdr_repositories_mock",  # herdr repository grouping fixtures (#13505), MOCK_HERDR_REPOSITORIES
    "project_headings_mock",  # same-named projects for the native By Project headings (#13558), MOCK_PROJECT_HEADINGS
    "github_mock",  # GitHub's sign-ins and MOCK_GITHUB_INBOX's thread page (/api/v1/connectors/github/…)
    "kb_selection_mock",  # Settings › Knowledge selection (/api/v1/kb/selection), MOCK_KB_SELECTION
    "project_tree_mock",  # the knowledge base's projects as a tree with milestones (#12927), MOCK_KB_TREE
    "project_lessons_mock",  # a project's lessons from finished tasks (/api/v1/kb/projects/{id}/lessons…), MOCK_PROJECT_LESSONS
    "mail_mock",  # mail accounts and write paths (/api/v1/mail/…, GET /mock/mail), MOCK_MAIL, MOCK_GOOGLE, MOCK_OUTLOOK
    "hub_update_mock",  # the hub's own update (/api/v1/hub/update…, GET /mock/hub_update), MOCK_HUB_UPDATE
    "imessage_mock",  # Messages' reader (/api/v1/imessage…), MOCK_IMESSAGE; the connector stays in mock_hub.py
    "saved_prompts_mock",  # Settings › Saved Prompts (/api/v1/assistant/saved-prompts…), MOCK_SAVED_PROMPTS
    "link_switch_mock",  # SSH to Persona Link scenarios (/api/v1/machines/{mid}/link-switch), MOCK_LINK_SWITCH
    "starred_mock",  # Starred messages (/api/v1/assistant/starred…), MOCK_STARRED
    "page_counts_mock",  # A paged session read with its counts, MOCK_PAGE_COUNTS
    "task_skip_mock",  # One scheduled occurrence, MOCK_SKIP_OCCURRENCE
    "outside_directions_mock",  # conversationless reporting directions (#8236), MOCK_OUTSIDE_DIRECTIONS
    "direction_sessions_mock",  # Move To… and one direction's page (#11516), MOCK_DIRECTION_SESSIONS
    "dock_mock",  # more of the main conversation's tasks waiting in the To Decide pager (#12831), MOCK_DOCK
    "meeting_finishing_mock",  # a meeting's final pass in progress for shots of the minutes line (#13461), MOCK_MEETING_FINISHING
    "extension_scene_mock",  # UI version 11's scene on a made-up Home page (#12544), MOCK_EXTENSION_SCENE
    "space_folders_mock",  # two of the MacBook's spaces with one name, in two folders (#13522), MOCK_SPACE_FOLDERS
    "extension_map_mock",  # UI version 12's map page, overview card, and tool_views (#14730), MOCK_EXTENSION_MAP
    "spaces_mock",  # domain spaces: tracks, kinds, SPACE.md, categories, extension trust (#13500), MOCK_SPACES
    "question_rail_mock",  # the main chat's question rail states and read marks (#13021), MOCK_QUESTION_RAIL
    "hub_modules_mock",  # a hub that publishes module flags: `modules` and `module.<id>` (#13849), MOCK_HUB_MODULES
    "usage_map_mock",  # the disk usage map: scan, trash, cleanup and an offload provider (#14576), MOCK_USAGE_MAP
    "machine_load_mock",  # moderate CPU with work queued on some cores: MOCK_LOAD_QUEUE (#12519)
    "orca_stopped_mock",  # Orca installed and not running, or incompatible, in the engine rows (#17037), MOCK_ORCA_STOPPED
    "todo_wait_reasons_mock",  # to-dos waiting for different reasons with the hub's typed `wait` (#16168), MOCK_TODO_WAIT_REASONS
    "dashboards_mock",  # Dashboards: the -pages places and pages, the `dashboards` switch (#15017), MOCK_DASHBOARDS
    "kb_chat_inbox_mock",  # a chat's Inbox items on its knowledge page and its items' kb_chat (#15356), MOCK_KB_CHAT_INBOX
    "command_input_mock",  # an Antigravity command waiting at a prompt (#14527), MOCK_COMMAND_INPUT
    "recording_minutes_debates_mock",  # disagreements in meeting minutes (#16728), POST /mock/recording_minutes_debates
    "recording_speakers_mock",  # meeting speakers samples and edit route (#17674), POST /api/v1/recordings/{id}/speakers
    "orca_chat_launchers_mock",  # the server's Orca chat launchers in New Agent (#17047), MOCK_ORCA_CHAT_LAUNCHERS
    "orca_chat_starts_mock",  # the server's "Start new Agents as Orca chats" switch (#17650), MOCK_ORCA_CHAT_STARTS
]

METHODS = ("GET", "POST", "PUT", "PATCH", "DELETE")


class Hub:
    """mock_hub.py's module globals, each read when used, so a global the hub rebinds later is the one seen.

    The ones domains use most: SESSIONS, ITEMS, MACHINES, INTERACTIONS and TODOS (the fixtures), CLIENTS (socket ->
    {"send", "features", "role", "close"}, under FAULT_LOCK), broadcast(msg) (every open socket), push_machine(id),
    sess(...), it(...), ago(...) and NOW. `hub.NAME = value` rebinds mock_hub.py's global."""

    def __init__(self, namespace):
        object.__setattr__(self, "_namespace", namespace)

    def __getattr__(self, name):
        try:
            return self._namespace[name]
        except KeyError:
            raise AttributeError("mock_hub.py has no global %r" % name) from None

    def __setattr__(self, name, value):
        self._namespace[name] = value


class Request:
    """The HTTP request a route answers: method, path (without the query), query (parse_qs), match (the pattern's
    re.Match, for its groups) and body (bytes; b"" for a GET)."""

    __slots__ = ("method", "path", "query", "match", "body")

    def __init__(self, method, path, query, match, body):
        self.method, self.path, self.query, self.match, self.body = method, path, query, match, body

    def json(self):
        """The body as JSON ({} when empty); ValueError when it is not JSON."""
        return json.loads(self.body or b"{}")


class Socket:
    """An app's WebSocket as message and close handlers see it: connection (the socket, CLIENTS' key), send(obj),
    client (its CLIENTS entry: role, features declared in client_hello…) and asked (the ?features= it connected with)."""

    __slots__ = ("connection", "send", "client", "asked")

    def __init__(self, connection, send, client, asked):
        self.connection, self.send, self.client, self.asked = connection, send, client, asked


class Registry:
    """What the loaded domains registered, and what mock_hub.py asks of it at its central points."""

    def __init__(self, namespace):
        self.hub = Hub(namespace)
        self.loaded = []  # module names, in load order
        self.help_lines = []  # MOCK_HELP lines, for mock_hub.py --help
        self._routes = {m: [] for m in METHODS}  # method -> [(compiled pattern, handler)]
        self._messages = {}  # WebSocket message type -> [handler]
        self._closers = []  # (domain name, handler)
        self._features = []  # ("add" | "drop", names, when)
        self._resets = []

    # ---- mock_hub.py's side ----

    def http(self, h, method, path, body=b""):
        """Answers h's request with the first route of `method` whose pattern matches the whole path and whose handler
        does not return False. True when one answered, or when the hub is "down" for a path outside /mock/ (the
        connection closes unanswered, h.bad_network); False leaves the request to mock_hub.py."""
        faults_checked = path.startswith("/mock/")  # test controls never see the faults, as in mock_hub.py
        for pattern, handler in self._routes.get(method, ()):
            match = pattern.fullmatch(path)
            if match is None:
                continue
            if not faults_checked:
                # mock_hub.py's H.bad_network acts once a request, so a route that hands the request on does not make
                # the built-in one wait the delay again.
                faults_checked = True
                bad_network = getattr(h, "bad_network", None)
                if bad_network is not None and bad_network():
                    return True
            if handler(h, Request(method, path, parse_qs(urlparse(h.path).query), match, body)) is not False:
                return True
        return False

    def socket(self, connection, send, client, asked):
        """The Socket an app's WebSocket connection is known by to the handlers, made once per connection."""
        return Socket(connection, send, client, asked)

    def ws_message(self, ws, msg):
        """Hands msg to the handlers of its type in order; True when one took it (did not return False)."""
        kind = msg.get("type")
        if not isinstance(kind, str):
            return False
        for handler in self._messages.get(kind, ()):
            if handler(ws, msg) is not False:
                return True
        return False

    def ws_closed(self, ws):
        """The app's socket closed: every close handler, in order. One that raises is logged on stderr and the others
        still run, as does the rest of mock_hub.py's cleanup of the socket."""
        for name, handler in self._closers:
            try:
                handler(ws)
            except Exception:
                print("mock: domain %s: its WebSocket close handler failed; the others still run" % name,
                      file=sys.stderr, flush=True)
                traceback.print_exc()

    def features(self, listed):
        """The hub's feature list with every domain's additions and removals applied, in registration order."""
        out = list(listed)
        for op, names, when in self._features:
            if when is not None and not when():
                continue
            if op == "add":
                out += [name for name in names if name not in out]
            else:
                out = [name for name in out if name not in names]
        return out

    def reset(self):
        """GET /mock/reset: every domain's hook, in order, after mock_hub.py's own reset."""
        for hook in self._resets:
            hook()


class Domain:
    """What a domain module's register(d) gets: the registry's verbs, and d.hub (mock_hub.py's globals, Hub)."""

    def __init__(self, registry, name):
        self._registry, self.name = registry, name

    @property
    def hub(self):
        return self._registry.hub

    def route(self, methods, pattern, handler):
        """handler(h, req) answers `methods` (GET, POST, PUT, PATCH or DELETE, or a list of them) on the paths the
        regular expression `pattern` matches whole. h is mock_hub.py's request handler (h.send_json(obj, status=200),
        h.headers); req is a Request. Returning False hands the request on."""
        for method in [methods] if isinstance(methods, str) else methods:
            if method not in METHODS:
                raise ValueError("%s: no such method %r (one of %s)" % (self.name, method, ", ".join(METHODS)))
            self._registry._routes[method].append((re.compile(pattern), handler))

    def ws(self, msg_type, handler):
        """handler(ws, msg) takes the WebSocket messages of msg_type from an app; returning False hands one on."""
        self._registry._messages.setdefault(msg_type, []).append(handler)

    def on_ws_close(self, handler):
        """handler(ws) runs when an app's WebSocket closes (an exception it raises is logged, not passed on)."""
        self._registry._closers.append((self.name, handler))

    def feature(self, *names, when=None):
        """Lists `names` in the hello and /api/v1/health (when when() is true, asked each time; once each)."""
        self._registry._features.append(("add", names, when))

    def drop_feature(self, *names, when=None):
        """Leaves `names` out of the features (when when() is true), as an older hub would."""
        self._registry._features.append(("drop", names, when))

    def help(self, *lines):
        """Lines for mock_hub.py --help: each "MOCK_SWITCH=values  what it does"."""
        self._registry.help_lines.extend(lines)

    def on_reset(self, hook):
        """hook() runs on GET /mock/reset, after mock_hub.py's own reset (so it can put back what that rebuilt)."""
        self._registry._resets.append(hook)


def load(namespace, names=None):
    """Imports each domain module (DOMAINS unless `names`) in order and calls its register(d); the Registry, on
    mock_hub.py's globals `namespace`.

    A module imported before runs again (importlib.reload), as each run of mock_hub.py makes its globals anew: a process
    that loads mock_hub.py more than once (runpy in a test) gets each domain's module-level state and switches afresh
    too, not what the last load left."""
    registry = Registry(namespace)
    for name in DOMAINS if names is None else names:
        module = importlib.reload(sys.modules[name]) if name in sys.modules else importlib.import_module(name)
        if not callable(getattr(module, "register", None)):
            raise TypeError("mock domain %s has no register(d)" % name)
        module.register(Domain(registry, name))
        registry.loaded.append(name)
    return registry
