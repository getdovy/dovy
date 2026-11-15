# fish completion for dovy: commands and recipe names, never options.

function __dovy_dir
    if set -q XDG_DATA_HOME
        echo $XDG_DATA_HOME/dovy/catalog/recipes
    else
        echo $HOME/.local/share/dovy/catalog/recipes
    end
end

function __dovy_installed # installed recipes, except dovy, which apt removes
    dpkg-query -W -f '${X-Dovy-Recipe}\t${db:Status-Abbrev}\n' 2>/dev/null |
        awk -F'\t' '$1 != "" && $1 != "dovy" && $2 ~ /^ii/ { print $1 }' | sort -u
end

# __dovy_offer install|remove|show: names and descriptions, tab-separated.
function __dovy_offer --argument-names which
    set -l dir (__dovy_dir)
    set -l typed (commandline -opc)[3..-1]
    set -l installed (__dovy_installed)
    set -l pool
    switch $which
        case remove
            set pool $installed
        case '*'
            for f in $dir/*/recipe.toml
                set -a pool (basename (dirname $f))
            end
    end
    for n in $pool
        contains -- $n $typed; and continue
        test $which = install; and contains -- $n $installed; and continue
        set -l desc
        test -r $dir/$n/recipe.toml; and set desc (string replace -rf '^description = "(.*)"$' '$1' <$dir/$n/recipe.toml)
        printf '%s\t%s\n' $n "$desc"
    end
end

complete -c dovy -f
complete -c dovy -n __fish_use_subcommand -a install -d 'plan, confirm, install'
complete -c dovy -n __fish_use_subcommand -a remove -d 'plan, confirm, remove'
complete -c dovy -n __fish_use_subcommand -a update -d 'fetch and verify the catalog'
complete -c dovy -n __fish_use_subcommand -a upgrade -d 'one plan for everything'
complete -c dovy -n __fish_use_subcommand -a list -d 'the catalog'
complete -c dovy -n __fish_use_subcommand -a search -d 'search names and descriptions'
complete -c dovy -n __fish_use_subcommand -a show -d 'details of a recipe'
complete -c dovy -n __fish_use_subcommand -a check -d 'report problems'
complete -c dovy -n '__fish_seen_subcommand_from install' -a '(__dovy_offer install)'
complete -c dovy -n '__fish_seen_subcommand_from remove' -a '(__dovy_offer remove)'
complete -c dovy -n '__fish_seen_subcommand_from show; and test (count (commandline -opc)) -eq 2' -a '(__dovy_offer show)'
