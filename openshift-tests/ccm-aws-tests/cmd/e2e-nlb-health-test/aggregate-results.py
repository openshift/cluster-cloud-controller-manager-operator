#!/usr/bin/env python3
"""Aggregate NLB health-transition e2e stdout into summary tables and JSON.

Reads raw test logs from nlb-cases-res/*.txt, extracts the HEALTH TRANSITION
REPORT block from each file, and produces:
  - stdout summary table (drain × cluster variant), or per-run table (--full)
  - optional Markdown report (--markdown PATH)
  - JSON export (--json PATH)

Filter a single case/plan batch with --prefix, e.g.:
  python3 aggregate-results.py nlb-cases-res --prefix nlb-case12-plan26
  python3 aggregate-results.py nlb-cases-res --prefix nlb-case13-plan27

Plan 27+ TG config variants use dotted plan ids in filenames/log stems, e.g.
  nlb-case13-plan27.1-90s_use1_v1.txt  → plan label v27.1 (one summary table per variant)

Per-run counters (no aggregation) with --full:
  python3 aggregate-results.py nlb-cases-res --prefix nlb-case12-plan26 --full

Supports report formats:
  - legacy: SERVICE CONFIGURATION / TARGET GROUP CONFIGURATION (svc/* metadata)
  - current: E2E TEST METADATA + LOAD BALANCER CONFIGURATION (AWS API)
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import defaultdict
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Any

REPORT_MARKER = "HEALTH TRANSITION REPORT"
REPORT_END_MARKERS = (
    "\n\n  I0",  # glog line after report (typical OTE stdout)
    "\n  I0",
)

FILENAME_CASE_RE = re.compile(r"^nlb-case([\d.]+)")
FILENAME_META_RE = re.compile(
    r"^nlb-case(?P<case>\d+)-plan(?:[_-]?v?(?P<plan>\d+))(?:\.(?P<config>\d+))?-(?P<drain>\d+s)",
    re.I,
)
FILENAME_PLAN_RE = re.compile(r"plan[_-]?v?(\d+)", re.I)
FILENAME_DRAIN_RE = re.compile(r"plan(?:[_-]v?\d+(?:\.\d+)?-)?(\d+s)", re.I)
FILENAME_DRAIN_FALLBACK_RE = re.compile(r"-(\d+s)(?:_|\.txt)")
FILENAME_ITER_RE = re.compile(r"_v(\d+)\.txt$")
BATCH_PREFIX_RE = re.compile(
    r"nlb-case(?P<case>\d+)-plan(?:[_-]?v?(?P<plan>\d+))(?:\.(?P<config>\d+))?",
    re.I,
)
TEST_NAME_RE = re.compile(r'"name":\s*"(\[cloud-provider[^\]]+\][^"]+)"')
KAS_PATCH_RE = re.compile(
    r"patch lbCrossZone=\S+ tgDesDelay=(\d+) tgDesConnTerm=(\S+) "
    r"tgPreserveCIP=\S+ tgUnhealthyDelay=(\d+) tgUnhealthyConnTerm=(\S+)",
    re.I,
)

VARIANT_ALIASES = {
    "use1": "us-east-1",
    "usw1": "us-west-1",
    "euw1": "eu-west-1",
}

# Fixed-width stdout columns (region-only cluster labels).
COL_PLAN = 7
COL_DRAIN = 6
COL_CLUSTER = 11
COL_RUNS = 4
COL_REPRO = 5
COL_RATE = 6
COL_PRE_AVG = 10
COL_PRE_MAX = 10
COL_T_STOP = 10
COL_T_CYCLE = 11
COL_DOWN = 10
COL_REQ_RATE = 10
COL_REQ_PCT = 10


def cluster_label(variant: str) -> str:
    return VARIANT_ALIASES.get(variant, variant)


def summary_table_header() -> str:
    return (
        f"{'Plan':>{COL_PLAN}} {'Drain':>{COL_DRAIN}} {'Cluster':>{COL_CLUSTER}} "
        f"{'Runs':>{COL_RUNS}} {'Repro':>{COL_REPRO}} {'Rate':>{COL_RATE}} "
        f"{'PreRdz_avg':>{COL_PRE_AVG}} {'PreRdz_max':>{COL_PRE_MAX}} "
        f"{'T_stop_avg':>{COL_T_STOP}} {'T_cycle_avg':>{COL_T_CYCLE}} "
        f"{'Down_avg':>{COL_DOWN}} {'ReqAvgRate':>{COL_REQ_RATE}} "
        f"{'ReqPerc2xx':>{COL_REQ_PCT}}"
    )

# Plan 27 TG attribute validation — known config variant titles (filename .N suffix).
PLAN27_CONFIG_TITLES: dict[int, str] = {
    1: "TG drain delay 30s (tgDesDelay=30, tgUnhealthyDelay=30, conn_term=false)",
    2: "TG drain delay 90s (tgDesDelay=90, tgUnhealthyDelay=90, conn_term=false)",
    3: "Drain disabled, connection termination enabled (tgDesDelay=0, tgUnhealthyDelay=0, conn_term=true)",
}
SPURIOUS_PRE_READYZ_COUNT = 1
SPURIOUS_RESTART_UNHEALTHY_COUNT = 1


@dataclass
class RunResult:
    filename: str
    case: str | None = None
    plan: int | None = None
    plan_label: str = "?"
    config_variant: int | None = None
    config_title: str | None = None
    test_name: str | None = None
    drain: str | None = None
    variant: str = "use1"
    iteration: int | None = None
    scenario: str | None = None
    platform: str | None = None
    region: str | None = None
    topology: str | None = None
    drain_observe: str | None = None
    restart_mode: str | None = None
    cross_zone_lb: str | None = None
    conn_termination: str | None = None
    draining_interval: str | None = None
    preserve_client_ip: str | None = None
    pre_readyz: int = 0
    pre_readyz_effective: int = 0
    pre_readyz_spurious: bool = False
    restart_unhealthy_reqs: int | None = None
    restart_unhealthy_effective: int = 0
    restart_unhealthy_spurious: bool = False
    unhealthy_reqs: int = 0
    late_conn_reqs: int = 0
    total_reqs: int | None = None
    reqs_2xx: int | None = None
    errors: int | None = None
    req_duration: str | None = None
    avg_rate: str | None = None
    avg_rate_sec: float | None = None
    req_perc_2xx: float | None = None
    t_route_stop: str | None = None
    t_route_stop_sec: float | None = None
    t_container_restart: str | None = None
    t_container_restart_sec: float | None = None
    t_route_start: str | None = None
    t_route_start_sec: float | None = None
    t_tg_unhealthy: str | None = None
    t_tg_unhealthy_sec: float | None = None
    t_tg_healthy: str | None = None
    t_tg_healthy_sec: float | None = None
    t_total_cycle: str | None = None
    t_total_cycle_sec: float | None = None
    t_downtime_window: str | None = None
    t_downtime_window_sec: float | None = None
    verdict_bug: str | None = None
    reproduced: bool = False
    tg_states: list[str] = field(default_factory=list)
    t_tcp_up_from_t5_sec: float | None = None
    overlap_est_tcp_up_sec: float | None = None
    overlap_with_propagation: bool | None = None
    parse_errors: list[str] = field(default_factory=list)

    def group_key(self) -> tuple[str, str, str | None]:
        """Group by plan label, drain, cluster variant."""
        return (self.plan_label, self.drain or "?", self.variant)


def parse_duration(raw: str | None) -> float | None:
    """Parse '1m28.28s', '54.784s', or 'N/A' to seconds."""
    if not raw or raw.upper() == "N/A":
        return None
    total = 0.0
    matched = False
    for val, unit in re.findall(r"([\d.]+)(ms|h|m|s)", raw):
        matched = True
        v = float(val)
        if unit == "h":
            total += v * 3600
        elif unit == "m":
            total += v * 60
        elif unit == "ms":
            total += v / 1000.0
        else:
            total += v
    return round(total, 3) if matched else None


def sec_stats(values: list[float]) -> tuple[float, float, float]:
    if not values:
        return 0.0, 0.0, 0.0
    return sum(values) / len(values), min(values), max(values)


def effective_pre_readyz(raw: int) -> int:
    """Ignore a lone pre-readyz response (ctl in-place restart race)."""
    if raw == SPURIOUS_PRE_READYZ_COUNT:
        return 0
    return raw


def effective_restart_unhealthy(raw: int | None, pre_readyz_raw: int) -> int:
    """Ignore a lone [RESTART] unhealthy count when pre-readyz is also absent/spurious."""
    if raw is None:
        return 0
    if (
        raw == SPURIOUS_RESTART_UNHEALTHY_COUNT
        and pre_readyz_raw <= SPURIOUS_PRE_READYZ_COUNT
    ):
        return 0
    return raw


def extract_restart_unhealthy(report: str) -> int | None:
    m = re.search(
        r"\[RESTART\] Target node received (\d+) unhealthy/pre-readyz request",
        report,
    )
    return int(m.group(1)) if m else None


def extract_downtime_window(report: str) -> tuple[str | None, float | None]:
    """Return t8−t5 downtime (readyz 503 → readyz 200) from timeline delta."""
    m = re.search(r"t8\s+readyz→200\s+\[\+(\S+)\]", report)
    if m:
        raw = m.group(1)
        return raw, parse_duration(raw)
    return None, None


def parse_avg_rate(raw: str | None) -> float | None:
    """Parse '931.9 req/s' to requests-per-second."""
    if not raw:
        return None
    m = re.search(r"([\d.]+)", raw)
    return float(m.group(1)) if m else None


def compute_req_perc_2xx(total: int | None, reqs_2xx: int | None) -> float | None:
    if total and total > 0 and reqs_2xx is not None:
        return round(100.0 * reqs_2xx / total, 1)
    return None


def extract_request_statistics(report: str) -> dict[str, Any]:
    """Parse REQUEST STATISTICS block from a health transition report."""
    block_m = re.search(
        r"REQUEST STATISTICS\s*\n(.*?)(?:\n\s*REQUEST BREAKDOWN BY PHASE|\n\s*TIMELINE|\Z)",
        report,
        re.DOTALL,
    )
    if not block_m:
        return {}
    block = block_m.group(1)
    out: dict[str, Any] = {}
    m = re.search(r"^\s*Total:\s+(\d+)", block, re.MULTILINE)
    if m:
        out["total_reqs"] = int(m.group(1))
    m = re.search(r"^\s*2xx:\s+(\d+)", block, re.MULTILINE)
    if m:
        out["reqs_2xx"] = int(m.group(1))
    m = re.search(r"^\s*Errors:\s+(\d+)", block, re.MULTILINE)
    if m:
        out["errors"] = int(m.group(1))
    m = re.search(r"^\s*Duration:\s+(\S+)", block, re.MULTILINE)
    if m:
        out["req_duration"] = m.group(1)
    m = re.search(r"^\s*Avg rate:\s+(.+?)\s*$", block, re.MULTILINE)
    if m:
        out["avg_rate"] = m.group(1).strip()
    return out


def group_req_avg_rate(runs: list[RunResult]) -> float | None:
    vals = [r.avg_rate_sec for r in runs if r.avg_rate_sec is not None]
    if not vals:
        return None
    return sum(vals) / len(vals)


def group_req_perc_2xx(runs: list[RunResult]) -> float | None:
    total = sum(r.total_reqs or 0 for r in runs)
    reqs_2xx = sum(r.reqs_2xx or 0 for r in runs if r.reqs_2xx is not None)
    if total > 0 and any(r.reqs_2xx is not None for r in runs):
        return round(100.0 * reqs_2xx / total, 1)
    pcts = [r.req_perc_2xx for r in runs if r.req_perc_2xx is not None]
    if pcts:
        return round(sum(pcts) / len(pcts), 1)
    return None


def fmt_req_rate(rate: float | None) -> str:
    if rate is None:
        return "?"
    return f"{rate:.1f}"


def fmt_req_pct(pct: float | None) -> str:
    if pct is None:
        return "?"
    return f"{pct:.1f}%"


def parse_drain_seconds(drain: str | None) -> float | None:
    if not drain:
        return None
    m = re.match(r"(\d+)s?", drain)
    return float(m.group(1)) if m else None


CASE_DEFAULT_PLAN = {
    "9": 23,
    "9.1": 23,
    "9.2": 23,
    "9.3": 23,
    "9.4": 23,
    "10": 25,
    "10.1": 25,
    "10.2": 25,
    "10.3": 25,
    "11": 25,
    "12": 26,
    "13": 27,
}


def plan_label_for(plan: int | None, config_variant: int | None) -> str:
    if plan is None:
        return "?"
    if config_variant is not None:
        return f"v{plan}.{config_variant}"
    return f"v{plan}"


def config_title_for(
    plan: int | None,
    config_variant: int | None,
    test_name: str | None = None,
) -> str | None:
    if config_variant is None:
        return None
    if plan == 27 and config_variant in PLAN27_CONFIG_TITLES:
        return PLAN27_CONFIG_TITLES[config_variant]
    if test_name:
        m = KAS_PATCH_RE.search(test_name)
        if m:
            des_delay, des_conn, unh_delay, unh_conn = m.groups()
            return (
                f"KAS patch tgDesDelay={des_delay} tgDesConnTerm={des_conn} "
                f"tgUnhealthyDelay={unh_delay} tgUnhealthyConnTerm={unh_conn}"
            )
    return f"config variant {config_variant}"


def extract_test_name(text: str) -> str | None:
    m = TEST_NAME_RE.search(text)
    return m.group(1) if m else None


def parse_filename(name: str, test_name: str | None = None) -> dict[str, Any]:
    meta: dict[str, Any] = {"filename": name}

    m = FILENAME_META_RE.match(name)
    if m:
        meta["case"] = m.group("case")
        meta["plan"] = int(m.group("plan"))
        config = m.group("config")
        meta["config_variant"] = int(config) if config else None
        meta["drain"] = m.group("drain")
    else:
        m = FILENAME_CASE_RE.match(name)
        case = m.group(1) if m else None
        if case and "-" in case:
            case = case.split("-", 1)[0]
        meta["case"] = case

        m = FILENAME_PLAN_RE.search(name)
        if m:
            meta["plan"] = int(m.group(1))
        elif case and case in CASE_DEFAULT_PLAN:
            meta["plan"] = CASE_DEFAULT_PLAN[case]
        elif case and case.split(".")[0] in CASE_DEFAULT_PLAN:
            meta["plan"] = CASE_DEFAULT_PLAN[case.split(".")[0]]
        else:
            meta["plan"] = None

        meta["config_variant"] = None
        m = FILENAME_DRAIN_RE.search(name) or FILENAME_DRAIN_FALLBACK_RE.search(name)
        meta["drain"] = m.group(1) if m else None

    meta["plan_label"] = plan_label_for(meta.get("plan"), meta.get("config_variant"))
    meta["config_title"] = config_title_for(
        meta.get("plan"), meta.get("config_variant"), test_name
    )

    if "_use1_" in name or name.endswith("_use1.txt"):
        meta["variant"] = "use1"
    elif "_usw1_" in name or name.endswith("_usw1.txt"):
        meta["variant"] = "usw1"
    elif "_euw1_" in name or name.endswith("_euw1.txt"):
        meta["variant"] = "euw1"
    else:
        meta["variant"] = "use1"

    m = FILENAME_ITER_RE.search(name)
    meta["iteration"] = int(m.group(1)) if m else None
    return meta


def normalize_prefix(prefix: str) -> str:
    """Strip wildcards/path; return filename glob stem (e.g. nlb-case12-plan26)."""
    p = prefix.strip().replace("*", "")
    if not p:
        return ""
    return Path(p).name


def parse_batch_prefix(prefix: str) -> dict[str, Any]:
    """Parse case/plan from a batch prefix like nlb-case12-plan26 or nlb-case13-plan27."""
    slug = normalize_prefix(prefix)
    info: dict[str, Any] = {
        "batch_id": slug,
        "case": None,
        "plan": None,
        "config_variant": None,
        "plan_label": None,
    }
    m = BATCH_PREFIX_RE.search(slug)
    if m:
        info["case"] = m.group("case")
        info["plan"] = int(m.group("plan"))
        config = m.group("config")
        if config:
            info["config_variant"] = int(config)
        info["plan_label"] = plan_label_for(info["plan"], info["config_variant"])
    return info


def resolve_inputs(
    results_dir: Path,
    prefix: str | None,
) -> tuple[Path, str | None]:
    """Return (directory, normalized prefix stem or None)."""
    if not prefix:
        return results_dir, None

    raw = prefix.strip()
    path = Path(raw)
    if path.parent != Path(".") and str(path.parent) not in (".", ""):
        return path.parent, normalize_prefix(path.name)

    return results_dir, normalize_prefix(raw)


def default_output_paths(results_dir: Path, prefix: str | None) -> tuple[Path, Path]:
    if prefix:
        slug = normalize_prefix(prefix)
        return (
            results_dir / f"{slug}-summary.md",
            results_dir / f"{slug}-summary.json",
        )
    return (
        results_dir / "nlb-tests-summary.md",
        results_dir / "nlb-results-summary.json",
    )


def strip_test_framework_json(text: str) -> str:
    """Remove trailing OTE JSON blob that embeds a duplicate report string."""
    for marker in ("\n[\n  {", "\n[{"):
        idx = text.rfind(marker)
        if idx != -1:
            return text[:idx]
    return text


def extract_report_block(text: str) -> str:
    """Return the last HEALTH TRANSITION REPORT section from mixed stdout."""
    text = strip_test_framework_json(text)
    idx = text.rfind(REPORT_MARKER)
    if idx == -1:
        return ""

    # Walk back to the report banner line if marker appears mid-line.
    line_start = text.rfind("\n", 0, idx)
    if line_start == -1:
        line_start = 0
    else:
        line_start += 1

    chunk = text[line_start:]
    verdict_idx = chunk.find("\n  VERDICT")
    if verdict_idx != -1:
        rest = chunk[verdict_idx:]
        info = re.search(r"\[INFO\][^\n]*\n", rest)
        if info:
            chunk = chunk[: verdict_idx + info.end()]
        else:
            closing = chunk.find("\n  ═", verdict_idx)
            if closing != -1:
                chunk = chunk[: closing + 1]
    else:
        for marker in REPORT_END_MARKERS:
            end = chunk.find(marker)
            if end != -1:
                chunk = chunk[:end]
                break
    return chunk


def extract_kv(report: str, key: str) -> str | None:
    """Extract 'key: value' from report (AWS API or metadata lines)."""
    pat = rf"^\s*{re.escape(key)}:\s+(.+?)\s*$"
    m = re.search(pat, report, re.MULTILINE)
    return m.group(1).strip() if m else None


def extract_timing_metric(report: str, name: str) -> str | None:
    m = re.search(rf"^\s*{re.escape(name)}\s+(\S+)", report, re.MULTILINE)
    return m.group(1) if m else None


def parse_report(text: str, filename: str) -> RunResult | None:
    report = extract_report_block(text)
    if not report:
        return None

    test_name = extract_test_name(text)
    meta = parse_filename(filename, test_name)
    run = RunResult(
        filename=filename,
        case=meta.get("case"),
        plan=meta.get("plan"),
        plan_label=meta.get("plan_label", "?"),
        config_variant=meta.get("config_variant"),
        config_title=meta.get("config_title"),
        test_name=test_name,
        drain=meta.get("drain"),
        variant=meta.get("variant", "use1"),
        iteration=meta.get("iteration"),
    )

    m = re.search(r"HEALTH TRANSITION REPORT — Scenario (.+)", report)
    if m:
        run.scenario = m.group(1).strip()

    run.platform = extract_kv(report, "Platform")
    run.region = extract_kv(report, "Region")
    run.topology = extract_kv(report, "Topology")

    run.drain_observe = (
        extract_kv(report, "e2e/shutdown-drain-observe")
        or extract_kv(report, "svc/shutdown-drain-observe")
        or run.drain
    )
    run.restart_mode = extract_kv(report, "e2e/restart-mode") or extract_kv(
        report, "svc/restart-mode"
    )

    # AWS API — prefer DescribeLoadBalancerAttributes, fall back to TG attrs (legacy).
    run.cross_zone_lb = extract_kv(report, "load_balancing.cross_zone.enabled")
    # In new reports cross_zone appears twice; LB attrs section has the effective value.
    lb_attr_match = re.search(
        r"--- DescribeLoadBalancerAttributes ---\s*\n"
        r"(?:\s+.+\n)*?"
        r"\s+load_balancing\.cross_zone\.enabled:\s+(\S+)",
        report,
    )
    if lb_attr_match:
        run.cross_zone_lb = lb_attr_match.group(1)

    run.conn_termination = extract_kv(
        report, "target_health_state.unhealthy.connection_termination.enabled"
    )
    run.draining_interval = extract_kv(
        report, "target_health_state.unhealthy.draining_interval_seconds"
    )
    run.preserve_client_ip = extract_kv(report, "preserve_client_ip.enabled")

    timing_fields = [
        ("t_route_stop", "T_route_stop"),
        ("t_container_restart", "T_container_restart"),
        ("t_route_start", "T_route_start"),
        ("t_tg_unhealthy", "T_tg_unhealthy"),
        ("t_tg_healthy", "T_tg_healthy"),
        ("t_total_cycle", "T_total_cycle"),
    ]
    for attr, metric in timing_fields:
        raw = extract_timing_metric(report, metric)
        setattr(run, attr, raw)
        setattr(run, attr + "_sec", parse_duration(raw))

    run.pre_readyz = int(extract_timing_metric(report, "Pre_readyz_reqs") or 0)
    run.restart_unhealthy_reqs = extract_restart_unhealthy(report)
    run.pre_readyz_effective = effective_pre_readyz(run.pre_readyz)
    run.restart_unhealthy_effective = effective_restart_unhealthy(
        run.restart_unhealthy_reqs, run.pre_readyz
    )
    run.pre_readyz_spurious = (
        run.pre_readyz == SPURIOUS_PRE_READYZ_COUNT and run.pre_readyz_effective == 0
    )
    run.restart_unhealthy_spurious = (
        run.restart_unhealthy_reqs == SPURIOUS_RESTART_UNHEALTHY_COUNT
        and run.restart_unhealthy_effective == 0
    )
    run.unhealthy_reqs = int(extract_timing_metric(report, "Unhealthy_reqs") or 0)
    run.late_conn_reqs = int(extract_timing_metric(report, "Late_conn_reqs") or 0)

    req_stats = extract_request_statistics(report)
    if req_stats.get("total_reqs") is not None:
        run.total_reqs = req_stats["total_reqs"]
    if req_stats.get("reqs_2xx") is not None:
        run.reqs_2xx = req_stats["reqs_2xx"]
    if req_stats.get("errors") is not None:
        run.errors = req_stats["errors"]
    if req_stats.get("req_duration"):
        run.req_duration = req_stats["req_duration"]
    if req_stats.get("avg_rate"):
        run.avg_rate = req_stats["avg_rate"]
        run.avg_rate_sec = parse_avg_rate(run.avg_rate)
    run.req_perc_2xx = compute_req_perc_2xx(run.total_reqs, run.reqs_2xx)

    m = re.search(r"\[BUG\]\s+(.+)", report)
    if m:
        run.verdict_bug = m.group(1).strip()

    run.tg_states = re.findall(r"TG\s+([\w.]+→[\w.]+)\s+target=", report)
    run.reproduced = run.pre_readyz_effective > 0

    run.t_downtime_window, run.t_downtime_window_sec = extract_downtime_window(report)

    m = re.search(r"t7\.1 ctl restart[^\n]*\[\+(\S+)\]", report)
    t71_from_t5 = parse_duration(m.group(1)) if m else None
    m = re.search(r"t7\.3 TCP up\s+\[\+(\S+)\]", report)
    t73_from_t71 = parse_duration(m.group(1)) if m else None
    if t71_from_t5 is not None:
        run.t_tcp_up_from_t5_sec = round(
            t71_from_t5 + (t73_from_t71 or 0.0), 3
        )

    drain_sec = parse_drain_seconds(run.drain_observe)
    if run.t_tg_unhealthy_sec is not None and drain_sec is not None:
        restart_sec = run.t_container_restart_sec or 0.0
        run.overlap_est_tcp_up_sec = round(
            run.t_tg_unhealthy_sec + drain_sec + restart_sec, 3
        )

    tcp_up = run.t_tcp_up_from_t5_sec or run.overlap_est_tcp_up_sec
    if run.t_route_stop_sec is not None and tcp_up is not None:
        run.overlap_with_propagation = run.t_route_stop_sec >= tcp_up

    if not run.drain and run.drain_observe:
        run.drain = run.drain_observe
    if run.t_route_stop is None:
        run.parse_errors.append("missing T_route_stop")
    if run.t_total_cycle is None:
        run.parse_errors.append("missing T_total_cycle")
    if run.t_downtime_window_sec is None:
        run.parse_errors.append("missing downtime window (t8−t5)")
    if run.cross_zone_lb is None:
        run.parse_errors.append("missing cross_zone attribute")

    return run


def load_results(results_dir: Path, prefix: str | None = None) -> tuple[list[RunResult], list[str], list[str]]:
    """Return (parsed runs, matched log paths, paths skipped without report)."""
    runs: list[RunResult] = []
    skipped: list[str] = []
    stem = normalize_prefix(prefix) if prefix else None
    glob_pattern = f"{stem}*.txt" if stem else "*.txt"
    paths = sorted(results_dir.glob(glob_pattern))
    if prefix and not paths:
        print(
            f"WARNING: no files match {results_dir / glob_pattern}",
            file=sys.stderr,
        )
    matched = [p.name for p in paths]
    for path in paths:
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError as exc:
            print(f"WARNING: skip {path.name}: {exc}", file=sys.stderr)
            skipped.append(path.name)
            continue
        if REPORT_MARKER not in text:
            skipped.append(path.name)
            continue
        run = parse_report(text, path.name)
        if run:
            runs.append(run)
    return runs, matched, skipped


def aggregate(runs: list[RunResult]) -> dict[tuple[str, str, str], list[RunResult]]:
    groups: dict[tuple[str, str, str], list[RunResult]] = defaultdict(list)
    for run in runs:
        groups[run.group_key()].append(run)
    return groups


def drain_sort_key(drain: str) -> float:
    sec = parse_drain_seconds(drain)
    return sec if sec is not None else 9999.0


def plan_label_sort_key(label: str) -> tuple[Any, ...]:
    m = re.match(r"v(\d+)(?:\.(\d+))?", label)
    if not m:
        return (9999, 9999, label)
    base = int(m.group(1))
    sub = int(m.group(2)) if m.group(2) else 0
    return (base, sub, label)


def runs_by_plan_label(runs: list[RunResult]) -> dict[str, list[RunResult]]:
    grouped: dict[str, list[RunResult]] = defaultdict(list)
    for run in runs:
        grouped[run.plan_label].append(run)
    return grouped


def run_sort_key(run: RunResult) -> tuple[Any, ...]:
    return (
        plan_label_sort_key(run.plan_label),
        drain_sort_key(run.drain or ""),
        run.variant,
        run.iteration or 0,
        run.filename,
    )


def run_id(run: RunResult) -> str:
    """Stable per-run identifier (log filename without extension)."""
    return Path(run.filename).stem


def fmt_sec(sec: float | None) -> str:
    if sec is None:
        return "?"
    return f"{sec:.1f}s"


def fmt_bool(value: bool | None) -> str:
    if value is None:
        return "?"
    return "yes" if value else "no"


def print_full_table(
    runs: list[RunResult],
    *,
    batch: dict[str, Any] | None = None,
) -> None:
    """Print one row per run with individual timers/counters (no aggregation)."""
    header = (
        f"{'#':>4} {'Run ID':<34} {'Plan':>{COL_PLAN}} {'Drain':>{COL_DRAIN}} "
        f"{'Cluster':>{COL_CLUSTER}} {'Repro':>{COL_REPRO}} {'PreRdz':>6} "
        f"{'T_stop':>9} {'T_cycle':>10} {'Down':>9} "
        f"{'Total':>8} {'2xx':>8} {'Avg rate':>12} {'Duration':>8}"
    )
    width = max(len(header), 100)
    print("=" * width)
    if batch and batch.get("batch_id"):
        case = batch.get("case") or "?"
        plan = batch.get("plan")
        plan_label = f"v{plan}" if plan else "?"
        print(
            f"NLB HEALTH TRANSITION — PER-RUN RESULTS "
            f"(batch {batch['batch_id']}, case {case}, plan {plan_label})"
        )
    else:
        print("NLB HEALTH TRANSITION — PER-RUN RESULTS")
    print("=" * width)
    if batch and batch.get("batch_id"):
        print(f"Prefix filter: {batch['batch_id']}*")
    print(f"Runs: {len(runs)}")
    print()
    print(header)
    print("-" * len(header))

    for idx, run in enumerate(sorted(runs, key=run_sort_key), start=1):
        plan = run.plan_label
        cluster = cluster_label(run.variant)
        repro = "REPRO" if run.reproduced else "no"
        total_s = str(run.total_reqs) if run.total_reqs is not None else "?"
        xx_s = str(run.reqs_2xx) if run.reqs_2xx is not None else "?"
        rate_s = (run.avg_rate or "?")[:12]
        dur_s = run.req_duration or "?"
        print(
            f"{idx:>4} {run_id(run):<34} {plan:>{COL_PLAN}} {run.drain or '?':>{COL_DRAIN}} "
            f"{cluster:>{COL_CLUSTER}} {repro:>{COL_REPRO}} {run.pre_readyz_effective:>6} "
            f"{fmt_sec(run.t_route_stop_sec):>9} {fmt_sec(run.t_total_cycle_sec):>10} "
            f"{fmt_sec(run.t_downtime_window_sec):>9} "
            f"{total_s:>8} {xx_s:>8} {rate_s:>12} {dur_s:>8}"
        )

    print()
    print(
        "KEY: # = row index; Run ID = log stem; Repro = effective Pre_readyz > 0; "
        "T_stop/T_cycle/Down = timing metrics; Total/2xx/ReqRate/Duration = REQUEST STATISTICS."
    )


def _print_summary_table_rows(
    groups: dict[tuple[str, str, str], list[RunResult]],
) -> None:
    header = summary_table_header()
    print(header)
    print("-" * len(header))

    for (plan, drain, variant), group_runs in sorted(
        groups.items(),
        key=lambda item: (plan_label_sort_key(item[0][0]), drain_sort_key(item[0][1]), item[0][2]),
    ):
        n = len(group_runs)
        repro = sum(1 for r in group_runs if r.reproduced)
        rate = f"{repro / n * 100:.0f}%" if n else "n/a"
        pre_vals = [r.pre_readyz_effective for r in group_runs]
        stop_vals = [r.t_route_stop_sec for r in group_runs if r.t_route_stop_sec is not None]
        cycle_vals = [r.t_total_cycle_sec for r in group_runs if r.t_total_cycle_sec is not None]
        down_vals = [
            r.t_downtime_window_sec for r in group_runs if r.t_downtime_window_sec is not None
        ]
        pre_avg = sum(pre_vals) / n
        pre_max = max(pre_vals)
        stop_avg, _, _ = sec_stats(stop_vals)
        cycle_avg, _, _ = sec_stats(cycle_vals)
        down_avg, _, _ = sec_stats(down_vals)
        req_avg_rate = group_req_avg_rate(group_runs)
        req_perc_2xx = group_req_perc_2xx(group_runs)

        cluster = cluster_label(variant)
        print(
            f"{plan:>{COL_PLAN}} {drain:>{COL_DRAIN}} {cluster:>{COL_CLUSTER}} "
            f"{n:>{COL_RUNS}} {repro:>{COL_REPRO}} {rate:>{COL_RATE}} "
            f"{pre_avg:{COL_PRE_AVG}.0f} {pre_max:{COL_PRE_MAX}d} "
            f"{stop_avg:{COL_T_STOP - 1}.1f}s {cycle_avg:{COL_T_CYCLE - 1}.1f}s "
            f"{down_avg:{COL_DOWN - 1}.1f}s "
            f"{fmt_req_rate(req_avg_rate):>{COL_REQ_RATE}} "
            f"{fmt_req_pct(req_perc_2xx):>{COL_REQ_PCT}}"
        )


def print_summary_table(
    groups: dict[tuple[str, str, str], list[RunResult]],
    *,
    batch: dict[str, Any] | None = None,
    section_title: str | None = None,
    show_banner: bool = True,
) -> None:
    table_w = len(summary_table_header())
    if show_banner:
        print("=" * table_w)
        if batch and batch.get("batch_id"):
            case = batch.get("case") or "?"
            plan = batch.get("plan")
            plan_label = batch.get("plan_label") or (f"v{plan}" if plan else "?")
            print(
                f"NLB HEALTH TRANSITION — BATCH {batch['batch_id']} "
                f"(case {case}, plan {plan_label})"
            )
        else:
            print("NLB HEALTH TRANSITION — AGGREGATED RESULTS")
        print("=" * table_w)
        if batch and batch.get("batch_id"):
            print(f"Prefix filter: {batch['batch_id']}*")
        print()

    if section_title:
        print(section_title)
        print()

    _print_summary_table_rows(groups)

    if show_banner:
        print()
        print(
            "KEY: Repro = effective Pre_readyz > 0 (raw count of 1 ignored as spurious); "
            "Rate = reproduction rate; T_stop = T_route_stop; T_cycle = T_total_cycle (t10−t5); "
            "Down = downtime window (t8−t5, readyz 503 → readyz 200); "
            "ReqAvgRate = mean Avg rate (req/s); ReqPerc2xx = weighted 2xx/total across runs."
        )


def print_summary_tables_by_config(
    runs: list[RunResult],
    *,
    batch: dict[str, Any] | None = None,
) -> None:
    by_label = runs_by_plan_label(runs)
    labels = sorted(by_label.keys(), key=plan_label_sort_key)
    multi = len(labels) > 1

    if multi:
        table_w = len(summary_table_header())
        print("=" * table_w)
        if batch and batch.get("batch_id"):
            case = batch.get("case") or "?"
            plan = batch.get("plan")
            print(
                f"NLB HEALTH TRANSITION — BATCH {batch['batch_id']} "
                f"(case {case}, plan v{plan}) — {len(labels)} TG config variant(s)"
            )
        else:
            print("NLB HEALTH TRANSITION — AGGREGATED RESULTS BY CONFIG")
        print("=" * table_w)
        if batch and batch.get("batch_id"):
            print(f"Prefix filter: {batch['batch_id']}*")
        print(f"Parsed runs: {len(runs)}")
        print()

    for idx, label in enumerate(labels):
        config_runs = by_label[label]
        sample = config_runs[0]
        section_title = None
        if multi:
            if sample.config_title:
                section_title = f"### {label} — {sample.config_title}"
            else:
                section_title = f"### {label}"

        if multi and idx > 0:
            print()
        print_summary_table(
            aggregate(config_runs),
            batch=batch,
            section_title=section_title,
            show_banner=not multi,
        )

    if multi:
        print()
        print(
            "KEY: One table per TG config variant (plan label vN.M). "
            "Repro = effective Pre_readyz > 0 (raw count of 1 ignored as spurious)."
        )


def _markdown_summary_rows(
    groups: dict[tuple[str, str, str], list[RunResult]],
) -> list[str]:
    rows: list[str] = []
    for (plan, drain, variant), group_runs in sorted(
        groups.items(),
        key=lambda item: (plan_label_sort_key(item[0][0]), drain_sort_key(item[0][1]), item[0][2]),
    ):
        n = len(group_runs)
        repro = sum(1 for r in group_runs if r.reproduced)
        rate = f"{repro / n * 100:.0f}%" if n else "n/a"
        pre_avg = sum(r.pre_readyz_effective for r in group_runs) / n
        pre_max = max(r.pre_readyz_effective for r in group_runs)
        stop_vals = [r.t_route_stop_sec for r in group_runs if r.t_route_stop_sec is not None]
        cycle_vals = [r.t_total_cycle_sec for r in group_runs if r.t_total_cycle_sec is not None]
        down_vals = [
            r.t_downtime_window_sec for r in group_runs if r.t_downtime_window_sec is not None
        ]
        stop_avg, _, _ = sec_stats(stop_vals)
        cycle_avg, _, _ = sec_stats(cycle_vals)
        down_avg, _, _ = sec_stats(down_vals)
        req_avg_rate = group_req_avg_rate(group_runs)
        req_perc_2xx = group_req_perc_2xx(group_runs)
        cross_zones = {r.cross_zone_lb for r in group_runs if r.cross_zone_lb}
        cross = ", ".join(sorted(cross_zones)) if cross_zones else "?"
        cluster = cluster_label(variant)
        rows.append(
            f"| {plan} | {drain} | {cluster} | {n} | {repro} | {rate} | "
            f"{pre_avg:.0f} | {pre_max} | {stop_avg:.1f}s | {cycle_avg:.1f}s | "
            f"{down_avg:.1f}s | {fmt_req_rate(req_avg_rate)} | {fmt_req_pct(req_perc_2xx)} | {cross} |"
        )
    return rows


def write_markdown_report(
    runs: list[RunResult],
    groups: dict[tuple[str, str, str], list[RunResult]],
    path: Path,
    *,
    batch: dict[str, Any] | None = None,
    source_glob: str | None = None,
) -> None:
    title = "# NLB Health Transition — Aggregated Results"
    if batch and batch.get("batch_id"):
        case = batch.get("case") or "?"
        plan = batch.get("plan")
        plan_label = f"v{plan}" if plan else "?"
        title = (
            f"# NLB Health Transition — Batch `{batch['batch_id']}` "
            f"(case {case}, plan {plan_label})"
        )

    source_desc = source_glob or f"{path.parent.name}/*.txt"
    lines: list[str] = [
        title,
        "",
        f"Generated from `{len(runs)}` report(s) matching `{source_desc}`.",
        "",
    ]

    by_label = runs_by_plan_label(runs)
    labels = sorted(by_label.keys(), key=plan_label_sort_key)
    if len(labels) > 1:
        lines.extend(["## Summary by TG config variant", ""])
        for label in labels:
            config_runs = by_label[label]
            sample = config_runs[0]
            heading = f"### {label}"
            if sample.config_title:
                heading += f" — {sample.config_title}"
            lines.extend([
                heading,
                "",
                "Grouped by **drain** × **cluster variant**.",
                "",
                "| Plan | Drain | Cluster | Runs | Repro | Rate | PreRdz avg | PreRdz max | "
                "T_stop avg | T_cycle avg | Down avg | ReqAvgRate | ReqPerc2xx | Cross-zone |",
                "|------|-------|---------|------|-------|------|------------|------------|"
                "------------|-------------|----------|------------|------------|------------|",
            ])
            lines.extend(_markdown_summary_rows(aggregate(config_runs)))
            lines.append("")
    else:
        lines.extend([
            "## Summary matrix",
            "",
            "Grouped by **drain** × **cluster variant** within the batch.",
            "",
            "| Plan | Drain | Cluster | Runs | Repro | Rate | PreRdz avg | PreRdz max | "
            "T_stop avg | T_cycle avg | Down avg | ReqAvgRate | ReqPerc2xx | Cross-zone |",
            "|------|-------|---------|------|-------|------|------------|------------|"
            "------------|-------------|----------|------------|------------|------------|",
        ])
        lines.extend(_markdown_summary_rows(groups))
        lines.append("")

    lines.extend(["", "## Per-run details", ""])
    for run in sorted(runs, key=run_sort_key):
        cluster = cluster_label(run.variant)
        repro = "REPRO" if run.reproduced else "no repro"
        lines.append(f"### `{run.filename}` — {repro}")
        lines.append("")
        if run.config_title:
            lines.append(f"- **Config:** {run.plan_label} — {run.config_title}")
        if run.scenario:
            lines.append(f"- **Scenario:** {run.scenario}")
        if run.test_name:
            lines.append(f"- **Test:** `{run.test_name}`")
        lines.append(
            f"- **Plan / drain / cluster / iter:** {run.plan_label} / {run.drain} / "
            f"{cluster} / v{run.iteration}"
        )
        lines.append(f"- **Region / topology:** {run.region} / {run.topology}")
        pre_line = f"- **Pre_readyz:** {run.pre_readyz_effective}"
        if run.pre_readyz != run.pre_readyz_effective:
            pre_line += f" (raw {run.pre_readyz}, spurious single-request artifact)"
        pre_line += (
            f" | **T_route_stop:** {run.t_route_stop} "
            f"| **Unhealthy_reqs:** {run.unhealthy_reqs}"
        )
        lines.append(pre_line)
        if run.restart_unhealthy_reqs is not None:
            restart_line = f"- **[RESTART] unhealthy reqs:** {run.restart_unhealthy_effective}"
            if run.restart_unhealthy_reqs != run.restart_unhealthy_effective:
                restart_line += (
                    f" (raw {run.restart_unhealthy_reqs}, spurious single-request artifact)"
                )
            lines.append(restart_line)
        lines.append(
            f"- **T_total_cycle:** {run.t_total_cycle} "
            f"| **Downtime window (t8−t5):** {run.t_downtime_window}"
        )
        if run.total_reqs is not None:
            lines.append(
                f"- **REQUEST STATISTICS:** Total={run.total_reqs} | 2xx={run.reqs_2xx} "
                f"| Avg rate={run.avg_rate or '?'} | Duration={run.req_duration or '?'} "
                f"| ReqPerc2xx={fmt_req_pct(run.req_perc_2xx)}"
            )
        lines.append(
            f"- **Cross-zone (LB):** {run.cross_zone_lb} | **conn_term:** {run.conn_termination} "
            f"| **draining:** {run.draining_interval}s | **preserve_client_ip:** {run.preserve_client_ip}"
        )
        if run.overlap_est_tcp_up_sec is not None:
            lines.append(
                f"- **Est. TCP-up after t5:** ~{run.overlap_est_tcp_up_sec}s | "
                f"**Overlap with propagation:** {run.overlap_with_propagation}"
            )
        if run.verdict_bug:
            lines.append(f"- **Verdict:** {run.verdict_bug}")
        if run.tg_states:
            lines.append(f"- **TG transitions:** {', '.join(run.tg_states)}")
        if run.parse_errors:
            lines.append(f"- **Parse warnings:** {', '.join(run.parse_errors)}")
        lines.append("")

    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def export_json(
    runs: list[RunResult],
    groups: dict[tuple[str, str, str], list[RunResult]],
    path: Path,
    *,
    batch: dict[str, Any] | None = None,
    source_glob: str | None = None,
) -> None:
    payload: dict[str, Any] = {
        "run_count": len(runs),
        "groups": {},
        "runs": [asdict(r) for r in runs],
    }
    if batch:
        payload["batch"] = batch
    if source_glob:
        payload["source_glob"] = source_glob
    by_label = runs_by_plan_label(runs)
    payload["config_groups"] = {}
    for label, config_runs in sorted(by_label.items(), key=lambda item: plan_label_sort_key(item[0])):
        config_groups = aggregate(config_runs)
        sample = config_runs[0]
        payload["config_groups"][label] = {
            "plan_label": label,
            "config_variant": sample.config_variant,
            "config_title": sample.config_title,
            "run_count": len(config_runs),
            "groups": {},
        }
        for key, group_runs in config_groups.items():
            plan, drain, variant = key
            group_label = f"{plan}_{drain}_{variant}"
            payload["config_groups"][label]["groups"][group_label] = {
                "plan": plan,
                "drain": drain,
                "variant": variant,
                "cluster": cluster_label(variant),
                "runs": len(group_runs),
                "reproduced": sum(1 for r in group_runs if r.reproduced),
                "filenames": [r.filename for r in group_runs],
            }
    for key, group_runs in groups.items():
        plan, drain, variant = key
        label = f"{plan}_{drain}_{variant}"
        payload["groups"][label] = {
            "plan": plan,
            "drain": drain,
            "variant": variant,
            "cluster": cluster_label(variant),
            "runs": len(group_runs),
            "reproduced": sum(1 for r in group_runs if r.reproduced),
            "pre_readyz_values": [r.pre_readyz for r in group_runs],
            "pre_readyz_effective_values": [r.pre_readyz_effective for r in group_runs],
            "restart_unhealthy_values": [r.restart_unhealthy_reqs for r in group_runs],
            "restart_unhealthy_effective_values": [
                r.restart_unhealthy_effective for r in group_runs
            ],
            "t_route_stop_sec_values": [
                r.t_route_stop_sec for r in group_runs if r.t_route_stop_sec is not None
            ],
            "t_total_cycle_sec_values": [
                r.t_total_cycle_sec for r in group_runs if r.t_total_cycle_sec is not None
            ],
            "t_total_cycle_sec_avg": sec_stats(
                [r.t_total_cycle_sec for r in group_runs if r.t_total_cycle_sec is not None]
            )[0],
            "t_downtime_window_sec_values": [
                r.t_downtime_window_sec
                for r in group_runs
                if r.t_downtime_window_sec is not None
            ],
            "t_downtime_window_sec_avg": sec_stats(
                [
                    r.t_downtime_window_sec
                    for r in group_runs
                    if r.t_downtime_window_sec is not None
                ]
            )[0],
            "req_avg_rate_avg": group_req_avg_rate(group_runs),
            "req_perc_2xx_weighted": group_req_perc_2xx(group_runs),
            "total_reqs_values": [r.total_reqs for r in group_runs],
            "reqs_2xx_values": [r.reqs_2xx for r in group_runs],
            "avg_rate_sec_values": [
                r.avg_rate_sec for r in group_runs if r.avg_rate_sec is not None
            ],
            "cross_zone_values": [r.cross_zone_lb for r in group_runs],
            "overlap_values": [
                r.overlap_with_propagation
                for r in group_runs
                if r.overlap_with_propagation is not None
            ],
            "filenames": [r.filename for r in group_runs],
        }
    path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "results_dir",
        nargs="?",
        default="nlb-cases-res",
        help="Directory containing nlb-case*.txt logs (default: nlb-cases-res)",
    )
    parser.add_argument(
        "--prefix",
        metavar="STEM",
        help=(
            "Only include logs whose filename starts with STEM "
            "(e.g. nlb-case12-plan26 or nlb-cases-res/nlb-case11-plan_v25). "
            "Default output: {STEM}-summary.md / .json in results_dir."
        ),
    )
    parser.add_argument(
        "--full",
        action="store_true",
        help=(
            "Print per-run table with individual timers/counters (Run ID index) "
            "instead of drain × cluster aggregated averages"
        ),
    )
    parser.add_argument(
        "--json",
        default=None,
        help="Write JSON summary (default: batch-specific or nlb-results-summary.json)",
    )
    parser.add_argument(
        "--markdown",
        default=None,
        help="Write Markdown report (default: batch-specific or nlb-tests-summary.md)",
    )
    parser.add_argument(
        "--no-json",
        action="store_true",
        help="Skip JSON export",
    )
    parser.add_argument(
        "--no-markdown",
        action="store_true",
        help="Skip Markdown export",
    )
    args = parser.parse_args()

    results_dir = Path(args.results_dir)
    if not results_dir.is_dir():
        print(f"ERROR: not a directory: {results_dir}", file=sys.stderr)
        return 1

    results_dir, prefix = resolve_inputs(results_dir, args.prefix)
    if not results_dir.is_dir():
        print(f"ERROR: not a directory: {results_dir}", file=sys.stderr)
        return 1

    batch = parse_batch_prefix(prefix) if prefix else None
    source_glob = f"{prefix}*.txt" if prefix else "*.txt"

    runs, matched, skipped = load_results(results_dir, prefix)
    if not runs:
        target = results_dir / source_glob
        if matched:
            print(
                f"No HEALTH TRANSITION REPORT found in {len(matched)} file(s) matching {target}",
                file=sys.stderr,
            )
            if skipped:
                preview = ", ".join(skipped[:5])
                suffix = f" (+{len(skipped) - 5} more)" if len(skipped) > 5 else ""
                print(f"  Skipped (no report): {preview}{suffix}", file=sys.stderr)
        else:
            print(f"No log files found matching {target}", file=sys.stderr)
        return 1

    groups = aggregate(runs)
    if args.full:
        print_full_table(runs, batch=batch)
    else:
        print_summary_tables_by_config(runs, batch=batch)

    default_md, default_json = default_output_paths(results_dir, prefix)
    md_path = Path(args.markdown) if args.markdown else default_md
    json_path = Path(args.json) if args.json else default_json

    if not args.no_json:
        json_path.parent.mkdir(parents=True, exist_ok=True)
        export_json(runs, groups, json_path, batch=batch, source_glob=source_glob)
        print(f"\nJSON exported to {json_path}")

    if not args.no_markdown:
        md_path.parent.mkdir(parents=True, exist_ok=True)
        write_markdown_report(
            runs, groups, md_path, batch=batch, source_glob=source_glob
        )
        print(f"Markdown report written to {md_path}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
