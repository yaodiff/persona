"""Real-user GitHub reviews on the exact current head (opt-in watcher policy).

When a repository enables ``respect_human_reviews``, a review submitted by a
trusted maintainer or collaborator can replace the watcher's own semantic
review for that exact head. The rules are deliberately narrow:

* only the current head counts -- a review on an older commit or with
  ``commit_id: null`` (draft/garbage-collected) never carries to a new head,
  so every pushed commit is re-evaluated from scratch;
* only trusted real user accounts count -- ``user.type == "User"`` with
  ``author_association`` in ``OWNER`` / ``MEMBER`` / ``COLLABORATOR``; bot and
  GitHub App reviews, external accounts, deleted users, the watcher's own
  authenticated identity, and the PR author's own replies never qualify
  (matched case-insensitively);
* pending drafts are ignored and dismissals clear the reviewer's prior state;
* per reviewer, non-pending reviews on the head are replayed chronologically
  by parsed UTC timestamp and id so inline ``COMMENTED`` thread replies do not
  erase an active ``APPROVED`` or ``CHANGES_REQUESTED`` decision.

The watcher's other gates (verification, privacy scan, linked issue,
mergeability and exact-head matching) are unaffected and stay mandatory.
"""
from pr_watch_common import SHA, WatchError, bounded, gh_json, github_time, run

APPROVED = "APPROVED"
CHANGES_REQUESTED = "CHANGES_REQUESTED"
COMMENTED = "COMMENTED"
DISMISSED = "DISMISSED"
PENDING = "PENDING"
EFFECTIVE_STATES = (APPROVED, CHANGES_REQUESTED, COMMENTED)
NEUTRAL_STATES = (DISMISSED, PENDING)
TRUSTED_ASSOCIATIONS = frozenset({"OWNER", "MEMBER", "COLLABORATOR"})

DECISION_APPROVED = "approved"
DECISION_CHANGES_REQUESTED = "changes_requested"
DECISION_COMMENTED = "commented"
DECISION_NONE = "none"

# A blocking human decision outranks an approval; an approval outranks a note.
_STATE_PRECEDENCE = {CHANGES_REQUESTED: 3, APPROVED: 2, COMMENTED: 1}
_DECISION_BY_STATE = {CHANGES_REQUESTED: DECISION_CHANGES_REQUESTED,
                      APPROVED: DECISION_APPROVED,
                      COMMENTED: DECISION_COMMENTED}


def fetch_reviews(repo, number):
    """Read every PR review through bounded REST pages."""
    data = gh_json(["api", "repos/%s/pulls/%s/reviews?per_page=100" % (repo, number),
                    "--paginate"], timeout=90)
    if not isinstance(data, list):
        raise WatchError("PR reviews endpoint did not return an array")
    return data


def authenticated_login():
    """The login whose reviews are the watcher's own automated publications.

    ``gh --jq`` emits a raw string rather than a JSON value, so this reads the
    process output directly instead of going through the JSON helper.
    """
    result = run(["gh", "api", "user", "--jq", ".login"], timeout=60)
    if result.returncode:
        raise WatchError("cannot read the authenticated GitHub login: " + bounded(result.stderr, 500))
    login = result.stdout.strip()
    if not login:
        raise WatchError("GitHub did not return the authenticated login")
    return login


def _review_record(raw):
    """Validate the transport shape; identity and trust filtering happen separately."""
    if not isinstance(raw, dict):
        raise WatchError("PR review record is not an object")
    review_id = raw.get("id")
    if not isinstance(review_id, int) or isinstance(review_id, bool) or review_id < 1:
        raise WatchError("PR review has no valid id")
    state = raw.get("state")
    if state not in EFFECTIVE_STATES + NEUTRAL_STATES:
        raise WatchError("PR review %s has an unknown state: %r" % (review_id, state))
    if "commit_id" not in raw:
        raise WatchError("PR review %s is missing commit_id" % review_id)
    commit_id = raw["commit_id"]
    if "user" not in raw:
        raise WatchError("PR review %s is missing user" % review_id)
    user = raw["user"]
    if user is not None and not isinstance(user, dict):
        raise WatchError("PR review %s has no valid reviewer identity" % review_id)
    if commit_id is None:
        # GitHub REST returns commit_id=null for PENDING draft reviews and
        # reviews whose commit was garbage-collected after a force-push; they
        # can never match the exact head, so ignore them cleanly.
        return None
    if not isinstance(commit_id, str) or not SHA.fullmatch(commit_id):
        raise WatchError("PR review %s has no valid commit id" % review_id)
    if user is None:
        # A null user is a documented GitHub shape for deleted accounts; it can
        # never be a human credential, so it is ignored rather than accepted.
        return None
    login = user.get("login")
    user_type = user.get("type")
    if not isinstance(login, str) or not login.strip() or not isinstance(user_type, str):
        raise WatchError("PR review %s has no valid reviewer identity" % review_id)
    author_association = raw.get("author_association")
    if author_association is not None and not isinstance(author_association, str):
        raise WatchError("PR review %s has an invalid author association" % review_id)
    submitted_at = raw.get("submitted_at")
    if state != PENDING:
        if not isinstance(submitted_at, str) or not submitted_at:
            raise WatchError("PR review %s has no submission timestamp" % review_id)
        submitted_ts = github_time(submitted_at)
    else:
        submitted_at = submitted_at if isinstance(submitted_at, str) else ""
        submitted_ts = 0.0
    return {"id": review_id, "state": state, "commit_id": commit_id,
            "login": login.strip(), "user_type": user_type,
            "author_association": author_association or "",
            "submitted_at": submitted_at, "submitted_ts": submitted_ts}


def classify_human_reviews(raw_reviews, head, self_login, pr_author=None):
    """Classify real-user reviews bound to ``head``; pure and side-effect free.

    Returns ``{"decision": "none"}`` or a mapping carrying the deciding
    review's id, login, state, commit id and submission timestamp.
    """
    if not isinstance(head, str) or not SHA.fullmatch(head):
        raise WatchError("human review classification needs the exact head")
    if not isinstance(self_login, str) or not self_login.strip():
        raise WatchError("human review classification needs the authenticated login")
    if pr_author is not None and not isinstance(pr_author, str):
        raise WatchError("human review classification PR author must be a string")
    self_key = self_login.strip().casefold()
    author_key = pr_author.strip().casefold() if pr_author and pr_author.strip() else ""
    by_login = {}
    for raw in raw_reviews:
        record = _review_record(raw)
        if record is None:
            continue
        if record["commit_id"] != head:
            # Reviews on older heads never carry over to a new head.
            continue
        if record["state"] == PENDING:
            # Unpublished draft reviews never alter submitted review state.
            continue
        if record["user_type"] != "User":
            # Bot/GitHub App/Organization/Mannequin accounts are not humans.
            continue
        if record["author_association"] not in TRUSTED_ASSOCIATIONS:
            # Only repository owners, org members, and collaborators have
            # review authority; external accounts can neither approve nor park.
            continue
        login_key = record["login"].casefold()
        if login_key == self_key or (author_key and login_key == author_key):
            # The watcher's own automated comment-reviews and the PR author's
            # own thread replies never qualify as independent human reviews.
            continue
        by_login.setdefault(login_key, []).append(record)
    deciding = None
    for records in by_login.values():
        active = None
        for record in sorted(records, key=lambda r: (r["submitted_ts"], r["id"])):
            state = record["state"]
            if state == DISMISSED:
                active = None
            elif state in (APPROVED, CHANGES_REQUESTED):
                active = record
            elif state == COMMENTED:
                # Inline thread replies create COMMENTED reviews on the same
                # head; they must not clear an active APPROVED/CHANGES_REQUESTED.
                if active is None or active["state"] == COMMENTED:
                    active = record
        if active is None:
            continue
        if deciding is None or (
            _STATE_PRECEDENCE[active["state"]], active["submitted_ts"], active["id"]
        ) > (
            _STATE_PRECEDENCE[deciding["state"]], deciding["submitted_ts"], deciding["id"]
        ):
            deciding = active
    if deciding is None:
        return {"decision": DECISION_NONE}
    return {"decision": _DECISION_BY_STATE[deciding["state"]],
            "review": {"id": deciding["id"], "login": deciding["login"],
                       "state": deciding["state"], "commit_id": deciding["commit_id"],
                       "submitted_at": deciding["submitted_at"]}}
