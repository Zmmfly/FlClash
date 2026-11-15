"""Validate the Web-only evaluation boundary after API overlays are staged."""


def adapt_evaluate(root, replace_exact):
    common = root / 'lib/common/javascript.dart'
    source = common.read_text()
    if 'handleEvaluateCommitted' not in source or 'WebEvaluateService' not in source:
        raise RuntimeError('Missing the typed evaluation overlay')
    forbidden = ('package:rust_api/', 'evalClashScript', 'scriptEvaluator(')
    if any(value in source for value in forbidden):
        raise RuntimeError('Browser evaluation must not invoke a raw script bridge')
    if not (root / 'lib/web_evaluate/index.dart').is_file():
        raise RuntimeError('Missing the typed evaluation service')
