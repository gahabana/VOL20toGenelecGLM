#!/usr/bin/env python3
"""
Convert Claude Code JSONL conversation files to readable, grep-friendly markdown.

Usage:
    # Convert a single session
    python3 claude_jsonl_to_markdown.py ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/SESSION.jsonl

    # Convert all sessions for a project
    python3 claude_jsonl_to_markdown.py ~/.claude/projects/-Users-zh-git-VOL20toGenelecGLM/*.jsonl

    # Output to a specific directory
    python3 claude_jsonl_to_markdown.py --outdir ~/claude-logs/markdown *.jsonl

    # Include tool calls and results (verbose)
    python3 claude_jsonl_to_markdown.py --verbose SESSION.jsonl

    # Plain text output (no markdown formatting) for maximum grep-friendliness
    python3 claude_jsonl_to_markdown.py --plain SESSION.jsonl

JSONL location:
    ~/.claude/projects/<encoded-project-path>/<session-uuid>.jsonl
"""

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path


def parse_timestamp(timestamp_value):
    """Parse various timestamp formats from JSONL records."""
    if isinstance(timestamp_value, (int, float)):
        # Millisecond epoch
        if timestamp_value > 1e12:
            timestamp_value = timestamp_value / 1000
        return datetime.fromtimestamp(timestamp_value, tz=timezone.utc)
    elif isinstance(timestamp_value, str):
        try:
            return datetime.fromisoformat(timestamp_value.replace('Z', '+00:00'))
        except ValueError:
            return None
    return None


def extract_text_from_content(content):
    """Extract readable text from message content (string or list of blocks)."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        text_parts = []
        for block in content:
            if isinstance(block, dict):
                block_type = block.get('type', '')
                if block_type == 'text':
                    text_parts.append(block.get('text', ''))
                elif block_type == 'tool_use':
                    tool_name = block.get('name', 'unknown_tool')
                    tool_input = block.get('input', {})
                    # Summarize tool call
                    if tool_name == 'Bash':
                        command = tool_input.get('command', '')
                        text_parts.append(f"[Tool: Bash] {command}")
                    elif tool_name == 'Read':
                        file_path = tool_input.get('file_path', '')
                        text_parts.append(f"[Tool: Read] {file_path}")
                    elif tool_name == 'Edit':
                        file_path = tool_input.get('file_path', '')
                        text_parts.append(f"[Tool: Edit] {file_path}")
                    elif tool_name == 'Write':
                        file_path = tool_input.get('file_path', '')
                        text_parts.append(f"[Tool: Write] {file_path}")
                    elif tool_name == 'Grep':
                        pattern = tool_input.get('pattern', '')
                        text_parts.append(f"[Tool: Grep] pattern={pattern}")
                    elif tool_name == 'Glob':
                        pattern = tool_input.get('pattern', '')
                        text_parts.append(f"[Tool: Glob] pattern={pattern}")
                    else:
                        text_parts.append(f"[Tool: {tool_name}]")
                elif block_type == 'tool_result':
                    result_content = block.get('content', '')
                    if isinstance(result_content, str) and result_content:
                        # Truncate long tool results
                        preview = result_content[:500]
                        if len(result_content) > 500:
                            preview += f"\n... ({len(result_content)} chars total)"
                        text_parts.append(f"[Tool Result]\n{preview}")
                    elif isinstance(result_content, list):
                        for sub_block in result_content:
                            if isinstance(sub_block, dict) and sub_block.get('type') == 'text':
                                result_text = sub_block.get('text', '')
                                preview = result_text[:500]
                                if len(result_text) > 500:
                                    preview += f"\n... ({len(result_text)} chars total)"
                                text_parts.append(f"[Tool Result]\n{preview}")
                # Skip 'thinking' blocks (internal reasoning)
            elif isinstance(block, str):
                text_parts.append(block)
        return '\n'.join(text_parts)
    return str(content)


def extract_text_from_content_verbose(content):
    """Extract text including full tool call details."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        text_parts = []
        for block in content:
            if isinstance(block, dict):
                block_type = block.get('type', '')
                if block_type == 'text':
                    text_parts.append(block.get('text', ''))
                elif block_type == 'tool_use':
                    tool_name = block.get('name', 'unknown_tool')
                    tool_input = block.get('input', {})
                    input_json = json.dumps(tool_input, indent=2)
                    text_parts.append(f"[Tool: {tool_name}]\n```json\n{input_json}\n```")
                elif block_type == 'tool_result':
                    result_content = block.get('content', '')
                    if isinstance(result_content, str):
                        text_parts.append(f"[Tool Result]\n```\n{result_content}\n```")
                    elif isinstance(result_content, list):
                        for sub_block in result_content:
                            if isinstance(sub_block, dict) and sub_block.get('type') == 'text':
                                text_parts.append(f"[Tool Result]\n```\n{sub_block.get('text', '')}\n```")
            elif isinstance(block, str):
                text_parts.append(block)
        return '\n'.join(text_parts)
    return str(content)


def convert_jsonl_to_markdown(jsonl_path, verbose=False, plain=False):
    """Convert a single JSONL file to markdown/text content."""
    session_id = Path(jsonl_path).stem
    messages = []
    session_start = None
    session_project = None
    session_cwd = None
    session_branch = None

    extractor = extract_text_from_content_verbose if verbose else extract_text_from_content

    with open(jsonl_path, 'r', encoding='utf-8') as json_file:
        for line_number, line in enumerate(json_file, 1):
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue

            record_type = record.get('type', '')
            timestamp = parse_timestamp(record.get('timestamp'))

            if record_type == 'user':
                if session_start is None and timestamp:
                    session_start = timestamp
                if session_cwd is None:
                    session_cwd = record.get('cwd', '')
                if session_branch is None:
                    session_branch = record.get('gitBranch', '')

                message_content = record.get('message', {})
                if isinstance(message_content, dict):
                    content = message_content.get('content', '')
                else:
                    content = str(message_content)

                text = extractor(content) if isinstance(content, (list, dict)) else str(content)
                if text.strip():
                    messages.append({
                        'role': 'human',
                        'text': text.strip(),
                        'timestamp': timestamp,
                    })

            elif record_type == 'assistant':
                message_data = record.get('message', {})
                content = message_data.get('content', [])
                model = message_data.get('model', '')

                text = extractor(content)
                if text.strip():
                    messages.append({
                        'role': 'assistant',
                        'text': text.strip(),
                        'timestamp': timestamp,
                        'model': model,
                    })

            elif record_type == 'system':
                # Optionally capture system messages
                pass

    if not messages:
        return None, session_id

    # Build output
    lines = []

    if plain:
        # Plain text format -- maximum grep-friendliness
        lines.append(f"Session: {session_id}")
        if session_start:
            lines.append(f"Date: {session_start.strftime('%Y-%m-%d %H:%M:%S UTC')}")
        if session_cwd:
            lines.append(f"Directory: {session_cwd}")
        if session_branch:
            lines.append(f"Branch: {session_branch}")
        lines.append("=" * 72)
        lines.append("")

        for message in messages:
            role_label = "HUMAN" if message['role'] == 'human' else "CLAUDE"
            timestamp_str = ""
            if message.get('timestamp'):
                timestamp_str = f" [{message['timestamp'].strftime('%H:%M:%S')}]"
            model_str = ""
            if message.get('model'):
                model_str = f" ({message['model']})"

            lines.append(f"--- {role_label}{timestamp_str}{model_str} ---")
            lines.append(message['text'])
            lines.append("")
    else:
        # Markdown format
        lines.append(f"# Session: {session_id}")
        lines.append("")
        if session_start:
            lines.append(f"**Date**: {session_start.strftime('%Y-%m-%d %H:%M:%S UTC')}")
        if session_cwd:
            lines.append(f"**Directory**: {session_cwd}")
        if session_branch:
            lines.append(f"**Branch**: {session_branch}")
        if messages and messages[-1].get('model'):
            lines.append(f"**Model**: {messages[-1]['model']}")
        lines.append("")
        lines.append("---")
        lines.append("")

        for message in messages:
            timestamp_str = ""
            if message.get('timestamp'):
                timestamp_str = f" *{message['timestamp'].strftime('%H:%M:%S')}*"

            if message['role'] == 'human':
                lines.append(f"## Human{timestamp_str}")
            else:
                model_tag = ""
                if message.get('model'):
                    model_tag = f" `{message['model']}`"
                lines.append(f"## Claude{timestamp_str}{model_tag}")

            lines.append("")
            lines.append(message['text'])
            lines.append("")

    return '\n'.join(lines), session_id


def main():
    parser = argparse.ArgumentParser(
        description="Convert Claude Code JSONL conversations to readable markdown/text.",
        epilog="JSONL files are at: ~/.claude/projects/<project-path>/<session>.jsonl"
    )
    parser.add_argument(
        'files',
        nargs='+',
        help='One or more JSONL files to convert'
    )
    parser.add_argument(
        '--outdir', '-o',
        default=None,
        help='Output directory (default: print to stdout, or same dir as input with --write)'
    )
    parser.add_argument(
        '--write', '-w',
        action='store_true',
        help='Write output files (to --outdir or alongside input files)'
    )
    parser.add_argument(
        '--verbose', '-v',
        action='store_true',
        help='Include full tool call details'
    )
    parser.add_argument(
        '--plain',
        action='store_true',
        help='Plain text output (no markdown) for maximum grep-friendliness'
    )
    parser.add_argument(
        '--list', '-l',
        action='store_true',
        help='List sessions with summary info instead of converting'
    )

    args = parser.parse_args()

    if args.list:
        print(f"{'Session ID':<40} {'Date':<22} {'Messages':>8}  {'Size':>10}")
        print("-" * 84)
        for filepath in sorted(args.files):
            session_id = Path(filepath).stem
            file_size = os.path.getsize(filepath)
            message_count = 0
            first_timestamp = None
            with open(filepath, 'r') as json_file:
                for line in json_file:
                    try:
                        record = json.loads(line)
                        record_type = record.get('type', '')
                        if record_type in ('user', 'assistant'):
                            message_count += 1
                            if first_timestamp is None:
                                first_timestamp = parse_timestamp(record.get('timestamp'))
                    except json.JSONDecodeError:
                        continue
            date_str = first_timestamp.strftime('%Y-%m-%d %H:%M') if first_timestamp else 'unknown'
            size_str = f"{file_size / 1024:.0f}K" if file_size < 1024 * 1024 else f"{file_size / (1024*1024):.1f}M"
            print(f"{session_id:<40} {date_str:<22} {message_count:>8}  {size_str:>10}")
        return

    extension = '.txt' if args.plain else '.md'

    for filepath in args.files:
        if not os.path.exists(filepath):
            print(f"Warning: {filepath} not found, skipping", file=sys.stderr)
            continue

        content, session_id = convert_jsonl_to_markdown(
            filepath,
            verbose=args.verbose,
            plain=args.plain,
        )

        if content is None:
            print(f"Warning: {filepath} has no messages, skipping", file=sys.stderr)
            continue

        if args.write or args.outdir:
            if args.outdir:
                os.makedirs(args.outdir, exist_ok=True)
                output_path = os.path.join(args.outdir, f"{session_id}{extension}")
            else:
                output_path = os.path.splitext(filepath)[0] + extension

            with open(output_path, 'w', encoding='utf-8') as output_file:
                output_file.write(content)
            print(f"Wrote: {output_path}", file=sys.stderr)
        else:
            # Print to stdout with a separator between files
            if len(args.files) > 1:
                print(f"\n{'=' * 72}")
                print(f"FILE: {filepath}")
                print(f"{'=' * 72}\n")
            print(content)


if __name__ == '__main__':
    main()
