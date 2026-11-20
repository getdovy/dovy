# bash completion for dovy: commands and recipe names, never options.

_dovy_recipes() { # every recipe in the catalog
	local d=${XDG_DATA_HOME:-$HOME/.local/share}/dovy/catalog/recipes
	[ -d "$d" ] && (cd "$d" && printf '%s\n' */ | sed -n 's#/$##p' | grep -v '^\*$')
}

_dovy_installed() { # installed recipes, except dovy, which apt removes
	dpkg-query -W -f '${X-Dovy-Recipe}\t${db:Status-Abbrev}\n' 2>/dev/null |
		awk -F'\t' '$1 != "" && $1 != "dovy" && $2 ~ /^ii/ { print $1 }' | LC_ALL=C sort -u
}

_dovy() {
	local cur=${COMP_WORDS[COMP_CWORD]} names w
	COMPREPLY=()
	if [ "$COMP_CWORD" -eq 1 ]; then
		COMPREPLY=($(compgen -W "install remove update upgrade list search show check clean" -- "$cur"))
		return
	fi
	case ${COMP_WORDS[1]} in
	install)
		if printf '%s\n' "${COMP_WORDS[@]}" | grep -qxF -- --reinstall; then
			names=$(_dovy_installed) # installed again
		else
			names=$(LC_ALL=C comm -23 <(_dovy_recipes | LC_ALL=C sort) <(_dovy_installed))
		fi
		;;
	remove) names=$(_dovy_installed) ;;
	show) [ "$COMP_CWORD" -eq 2 ] && names=$(_dovy_recipes) ;;
	esac
	[ -n "$names" ] || return
	for w in "${COMP_WORDS[@]:2:COMP_CWORD-2}"; do # a name on the line is not offered again
		names=$(printf '%s\n' $names | grep -vxF -- "$w")
	done
	COMPREPLY=($(compgen -W "$names" -- "$cur"))
}
complete -F _dovy dovy
