package rules

import "strings"

type suffixTrieNode struct {
	children map[string]*suffixTrieNode
	rule     string
}

type suffixTrie struct {
	root *suffixTrieNode
}

func newSuffixTrie() *suffixTrie {
	return &suffixTrie{
		root: &suffixTrieNode{children: map[string]*suffixTrieNode{}},
	}
}

func (t *suffixTrie) Insert(rule string) {
	if t == nil || rule == "" {
		return
	}
	node := t.root
	parts := strings.Split(strings.ToLower(rule), ".")
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part == "" {
			continue
		}
		child, ok := node.children[part]
		if !ok {
			child = &suffixTrieNode{children: map[string]*suffixTrieNode{}}
			node.children[part] = child
		}
		node = child
	}
	node.rule = rule
}

func (t *suffixTrie) Match(domain string) string {
	if t == nil || domain == "" {
		return ""
	}
	node := t.root
	parts := strings.Split(strings.ToLower(domain), ".")
	matched := ""
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		child, ok := node.children[part]
		if !ok {
			break
		}
		node = child
		if node.rule != "" {
			matched = node.rule
		}
	}
	return matched
}
