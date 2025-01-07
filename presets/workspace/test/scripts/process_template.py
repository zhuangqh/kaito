#!/usr/bin/env python3
import json
import yaml
import sys
import os
import argparse
from pathlib import Path

def get_project_root():
    # Get the directory containing the current script
    current_dir = Path(__file__).resolve().parent
    # Go up 4 levels to reach project root (from presets/workspace/test/scripts)
    return current_dir.parents[3]

def load_json_config():
    project_root = get_project_root()
    config_path = project_root / '.github' / 'e2e-preset-configs.json'
    with open(config_path, 'r') as f:
        data = json.load(f)
        return data['matrix']['image']  # Return the array of model configs

def load_template():
    project_root = get_project_root()
    template_path = project_root / 'presets' / 'workspace' / 'test' / 'manifests' / 'inference-tmpl' / 'manifest.yaml'
    with open(template_path, 'r') as f:
        return yaml.safe_load(f)

def process_model(model_name, runtime, repo=None, tag=None):
    configs = load_json_config()
    model_config = next((m for m in configs if m['name'] == model_name), None)

    if not model_config:
        print(f"Model {model_name} not found in configs", file=sys.stderr)
        sys.exit(1)

    if runtime not in model_config.get('runtimes', {}):
        print(f"Runtime {runtime} not configured for model {model_name}", file=sys.stderr)
        sys.exit(1)

    runtime_config = model_config['runtimes'][runtime]
    templates = load_template()

    # Choose template based on kind
    template_type = 'statefulset' if model_config.get('kind') == 'StatefulSet' else 'deployment'
    manifest_str = yaml.dump(templates[template_type])

    # Replace placeholders in template
    manifest_str = (
        manifest_str
        .replace('MODEL_NAME', model_name)
        .replace('RUNTIME_COMMAND', runtime_config['command'])
        .replace('GPU_COUNT', str(runtime_config['gpu_count']))
        .replace('NODE_POOL', model_config['node_pool'])
        .replace('NODE_COUNT', str(model_config['node-count']))
    )

    # Replace repo and tag if provided
    if repo:
        manifest_str = manifest_str.replace('REPO', repo)
    if tag:
        manifest_str = manifest_str.replace('TAG', tag)

    # Parse the template string back into YAML
    manifest = yaml.safe_load(manifest_str)

    # Generate service manifest
    service_template = templates['service']
    service_str = yaml.dump(service_template)
    service_str = service_str.replace('MODEL_NAME', model_name)
    service_manifest = yaml.safe_load(service_str)

    # Generate config manifest
    config_template = templates['config']
    config_str = yaml.dump(config_template)
    config_manifest = yaml.safe_load(config_str)

    # Print manifests to stdout
    yaml.dump(manifest, sys.stdout, default_flow_style=False)
    print('---')  # Document separator
    yaml.dump(service_manifest, sys.stdout, default_flow_style=False)
    print('---')  # Document separator
    yaml.dump(config_manifest, sys.stdout, default_flow_style=False)

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description='Process model template with optional repo and tag.')
    parser.add_argument('model_name', help='Name of the model')
    parser.add_argument('runtime', help='Runtime to use')
    parser.add_argument('--repo', help='Repository name to use instead of REPO_HERE')
    parser.add_argument('--tag', help='Tag to use instead of TAG_HERE')

    args = parser.parse_args()
    process_model(args.model_name, args.runtime, args.repo, args.tag)
