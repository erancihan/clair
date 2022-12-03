import React from "react";

export function SingInWith({ children = [] }): JSX.Element {
    return (
        React.createElement(
            'a',
            {
                className: 'inline-flex w-full justify-center rounded-md border border-gray-300 bg-white py-2 px-4 text-sm font-medium text-gray-500 shadow-sm hover:bg-gray-50'
            },
            [...children]
        )
    )
}