"""Conquest's TES API may take as long as a remote endpoint to answer.

Creating a task can forward it to a remote TES endpoint before Conquest
answers, which may take up to the endpoint's request_timeout (30s by
default). With py-tes's default timeout of 10s, one slow endpoint ended a
Snakemake run with a WorkflowError (ReadTimeout).
"""

import pytest

from snakemake_executor_plugin_conquest_tes import (
    DEFAULT_TES_CLIENT_TIMEOUT,
    tes_client_timeout,
)


def test_default_exceeds_the_remote_request_timeout(monkeypatch):
    monkeypatch.delenv("CONQUEST_TES_CLIENT_TIMEOUT", raising=False)
    assert tes_client_timeout() == DEFAULT_TES_CLIENT_TIMEOUT
    assert DEFAULT_TES_CLIENT_TIMEOUT > 30


@pytest.mark.parametrize(
    "value, expected",
    [("300", 300), (" 45 ", 45), ("", DEFAULT_TES_CLIENT_TIMEOUT), ("0", DEFAULT_TES_CLIENT_TIMEOUT), ("abc", DEFAULT_TES_CLIENT_TIMEOUT)],
)
def test_environment_sets_it(monkeypatch, value, expected):
    monkeypatch.setenv("CONQUEST_TES_CLIENT_TIMEOUT", value)
    assert tes_client_timeout() == expected
