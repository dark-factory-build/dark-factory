"""Shared pull-request publication footer contract."""
import re


APP_MARKER_TRAILER = re.compile(
    r"(?mi)^<!-- dark-factory-operation:([0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}):([0-9a-f]{64}) -->\s*\Z"
)
TERMINAL_FOOTER = re.compile(
    r"(?mi)^(Refs|Closes)[ \t]+(?:([A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}))?#([1-9][0-9]*)[ \t]*(?:\r?\n[ \t]*)*\Z"
)
FOOTER = re.compile(r"(?mi)^(?:Refs|Closes)[ \t]+(?:([A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}))?#([1-9][0-9]*)[ \t]*(?:\r?$)")


def terminal_footer(body):
    """Return the one source footer accepted by release routing, if present."""
    match = TERMINAL_FOOTER.search(body)
    if match is not None:
        return match
    marker = APP_MARKER_TRAILER.search(body)
    if marker is None:
        return None
    return TERMINAL_FOOTER.search(body[:marker.start()])
