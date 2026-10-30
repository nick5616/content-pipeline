// Reads Nickel Tools' content.ts (plus taxonomy.ts and profile.ts beside it)
// with the TypeScript compiler API and prints the data as JSON. It never
// executes the files: only literal values are read, so the React imports,
// components and helper functions in them don't matter. Identifiers (a
// component like BatchAnalyzerDescription) become {"$ident": name}.
// Comments inside or just above an entry come out as "$comments".
//
//   node extract.mjs <dir whose node_modules has typescript> <content.ts>
import { createRequire } from "node:module";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

const [tsDir, contentPath] = process.argv.slice(2);
const ts = createRequire(join(resolve(tsDir), "noop.js"))("typescript");

const parse = (path) =>
    ts.createSourceFile(path, readFileSync(path, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);

const unwrap = (n) => {
    while (n && (ts.isAsExpression(n) || ts.isSatisfiesExpression?.(n) || ts.isParenthesizedExpression(n) || ts.isTypeAssertionExpression(n)))
        n = n.expression;
    return n;
};

// commentsOf returns the comments inside and just above an entry. A comment
// between two // ---- lines above it is a section header, returned apart.
const commentsOf = (sf, node) => {
    const text = sf.getFullText();
    const out = [];
    const sections = [];
    const clean = (r) => {
        const c = text.slice(r.pos, r.end);
        return (c.startsWith("//") ? c.slice(2) : c.replace(/^\/\*+|\*+\/$/g, "")).trim();
    };
    const add = (ranges) =>
        (ranges ?? []).forEach((r) => {
            const c = clean(r);
            if (c && !/^[-=]+$/.test(c)) out.push(c);
        });
    let inRule = false;
    for (const r of ts.getLeadingCommentRanges(text, node.getFullStart()) ?? []) {
        const c = clean(r);
        if (/^[-=]+$/.test(c)) inRule = !inRule;
        else if (inRule) sections.push(c);
        else if (c) out.push(c);
    }
    const walk = (n) => {
        if (n !== node) {
            add(ts.getLeadingCommentRanges(text, n.getFullStart()));
        }
        add(ts.getTrailingCommentRanges(text, n.getEnd()));
        n.forEachChild(walk);
    };
    node.forEachChild(walk);
    return { comments: [...new Set(out)], sections };
};

const value = (sf, n, withComments) => {
    n = unwrap(n);
    if (!n) return null;
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) return n.text;
    if (ts.isNumericLiteral(n)) return Number(n.text);
    if (ts.isPrefixUnaryExpression(n) && n.operator === ts.SyntaxKind.MinusToken) return -value(sf, n.operand);
    if (n.kind === ts.SyntaxKind.TrueKeyword) return true;
    if (n.kind === ts.SyntaxKind.FalseKeyword) return false;
    if (n.kind === ts.SyntaxKind.NullKeyword) return null;
    if (ts.isIdentifier(n)) return { $ident: n.text };
    if (ts.isArrayLiteralExpression(n)) return n.elements.map((e) => value(sf, e, withComments));
    if (ts.isObjectLiteralExpression(n)) {
        const o = {};
        for (const p of n.properties) {
            if (ts.isPropertyAssignment(p)) {
                const key = ts.isIdentifier(p.name) || ts.isStringLiteral(p.name) ? p.name.text : p.name.getText(sf);
                o[key] = value(sf, p.initializer, false);
            } else if (ts.isShorthandPropertyAssignment(p)) {
                o[p.name.text] = { $ident: p.name.text };
            } else {
                o[`$unsupported:${p.getText(sf).slice(0, 40)}`] = true;
            }
        }
        if (withComments) {
            const { comments, sections } = commentsOf(sf, n);
            if (comments.length) o.$comments = comments;
            if (sections.length) o.$section = sections.join(" ");
        }
        return o;
    }
    return { $unsupported: n.getText(sf).slice(0, 80) };
};

const findVar = (sf, name) => {
    let found;
    const walk = (n) => {
        if (found) return;
        if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && n.name.text === name) found = n.initializer;
        n.forEachChild(walk);
    };
    walk(sf);
    return found;
};

const content = parse(contentPath);
const system = unwrap(findVar(content, "NICKEL_SYSTEM"));
if (!system || !ts.isObjectLiteralExpression(system)) throw new Error("no NICKEL_SYSTEM object literal in " + contentPath);
const prop = (obj, name) => obj.properties.find((p) => ts.isPropertyAssignment(p) && p.name.getText(content) === name)?.initializer;
const entries = unwrap(prop(system, "content"));
const out = {
    content: entries.elements.map((e) => value(content, e, true)),
    featured: value(content, prop(system, "featured")),
    icons: value(content, findVar(content, "iconMap")),
    taxonomy: {},
    profile: null,
};
const dir = dirname(contentPath);
if (existsSync(join(dir, "taxonomy.ts"))) {
    const tax = parse(join(dir, "taxonomy.ts"));
    for (const name of ["CATEGORIES", "STATUSES", "TECHNOLOGIES", "MEDIUMS", "TAGS"]) {
        const v = findVar(tax, name);
        if (v) out.taxonomy[name] = value(tax, v);
    }
}
if (existsSync(join(dir, "profile.ts"))) {
    out.profile = value(parse(join(dir, "profile.ts")), findVar(parse(join(dir, "profile.ts")), "PROFILE"));
}
process.stdout.write(JSON.stringify(out, null, 2) + "\n");
